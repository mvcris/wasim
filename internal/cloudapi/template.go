package cloudapi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Template catalog values, in the casing Graph returns them.
const (
	TemplateStatusApproved = "APPROVED"

	FormatNamed      = "NAMED"
	FormatPositional = "POSITIONAL"

	componentBody    = "BODY"
	componentHeader  = "HEADER"
	componentFooter  = "FOOTER"
	componentButtons = "BUTTONS"

	buttonQuickReply = "QUICK_REPLY"
)

var placeholderPattern = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// TemplateDefinition is a catalog entry, shaped like the Graph listing.
type TemplateDefinition struct {
	ID              string              `json:"id,omitempty"`
	Name            string              `json:"name"`
	Language        string              `json:"language"`
	Status          string              `json:"status,omitempty"`
	Category        string              `json:"category,omitempty"`
	ParameterFormat string              `json:"parameter_format,omitempty"`
	Components      []TemplateComponent `json:"components"`
}

// TemplateComponent is one block of a catalog entry.
type TemplateComponent struct {
	Type    string           `json:"type"`
	Format  string           `json:"format,omitempty"`
	Text    string           `json:"text,omitempty"`
	Buttons []TemplateButton `json:"buttons,omitempty"`
	Example map[string]any   `json:"example,omitempty"`
}

// TemplateButton is one button of a catalog entry.
type TemplateButton struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	URL         string `json:"url,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
}

// TemplateContent is the `template` object of a send request.
type TemplateContent struct {
	Name       string             `json:"name"`
	Language   TemplateLanguage   `json:"language"`
	Components []ContentComponent `json:"components,omitempty"`
}

// TemplateLanguage selects the translation.
type TemplateLanguage struct {
	Code string `json:"code"`
}

// ContentComponent carries the parameters of one template block.
type ContentComponent struct {
	Type       string             `json:"type"`
	SubType    string             `json:"sub_type,omitempty"`
	Index      any                `json:"index,omitempty"`
	Parameters []ContentParameter `json:"parameters,omitempty"`
}

// ContentParameter is one parameter value.
type ContentParameter struct {
	Type          string        `json:"type"`
	ParameterName string        `json:"parameter_name,omitempty"`
	Text          string        `json:"text,omitempty"`
	Payload       string        `json:"payload,omitempty"`
	Currency      *FallbackText `json:"currency,omitempty"`
	DateTime      *FallbackText `json:"date_time,omitempty"`
}

// FallbackText is the part of currency and date_time parameters a text render
// can use.
type FallbackText struct {
	FallbackValue string `json:"fallback_value"`
}

func (p ContentParameter) value() string {
	switch {
	case p.Currency != nil:
		return p.Currency.FallbackValue
	case p.DateTime != nil:
		return p.DateTime.FallbackValue
	default:
		return p.Text
	}
}

// Format returns the parameter grammar, inferring it from the placeholders when
// the catalog entry does not state one.
func (d TemplateDefinition) Format() string {
	if format := strings.ToUpper(d.ParameterFormat); format == FormatNamed || format == FormatPositional {
		return format
	}
	for _, component := range d.Components {
		for _, name := range placeholders(component.Text) {
			if _, err := strconv.Atoi(name); err != nil {
				return FormatNamed
			}
		}
	}
	return FormatPositional
}

// IsApproved reports whether the template can be sent.
func (d TemplateDefinition) IsApproved() bool {
	return strings.EqualFold(d.Status, TemplateStatusApproved)
}

// ValidateTemplateParameters checks a send against its catalog entry. A wrong
// grammar (named values for a positional template or the reverse) reports
// 132000, the code production consumers observe for that mistake, rather than
// the 132012 the reference's wording suggests.
func ValidateTemplateParameters(definition TemplateDefinition, content TemplateContent) *APIError {
	format := definition.Format()
	for _, component := range definition.Components {
		kind := strings.ToUpper(component.Type)
		if kind != componentBody && kind != componentHeader {
			continue
		}
		if kind == componentHeader && component.Format != "" && !strings.EqualFold(component.Format, "TEXT") {
			continue
		}
		parameters := parametersFor(content, kind)
		expected := placeholders(component.Text)
		label := strings.ToLower(kind)
		if len(parameters) != len(expected) {
			return NewError(CodeTemplateParamCount, fmt.Sprintf(
				"%s: number of localizable_params (%d) does not match the expected number of params (%d)",
				label, len(parameters), len(expected),
			))
		}
		if err := validateParameterNames(label, format, expected, parameters); err != nil {
			return err
		}
		for _, parameter := range parameters {
			if parameter.value() == "" {
				return NewError(CodeMissingParameter, label+": a parameter value is empty")
			}
		}
	}
	return nil
}

func validateParameterNames(label, format string, expected []string, parameters []ContentParameter) *APIError {
	if format == FormatPositional {
		for _, parameter := range parameters {
			if parameter.ParameterName != "" {
				return NewError(CodeTemplateParamCount, label+": this template uses positional parameters, parameter_name must not be set")
			}
		}
		return nil
	}
	missing := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		missing[name] = struct{}{}
	}
	for _, parameter := range parameters {
		if _, ok := missing[parameter.ParameterName]; !ok {
			return NewError(CodeTemplateParamCount, fmt.Sprintf("%s: unexpected or repeated parameter_name %q", label, parameter.ParameterName))
		}
		delete(missing, parameter.ParameterName)
	}
	return nil
}

// RenderTemplate returns what the recipient would see.
func RenderTemplate(definition TemplateDefinition, content TemplateContent) Rendered {
	format := definition.Format()
	rendered := Rendered{TemplateName: definition.Name}
	for _, component := range definition.Components {
		kind := strings.ToUpper(component.Type)
		switch kind {
		case componentHeader:
			rendered.Header = substitute(component.Text, parametersFor(content, kind), format)
		case componentBody:
			rendered.Body = substitute(component.Text, parametersFor(content, kind), format)
		case componentFooter:
			rendered.Footer = component.Text
		case componentButtons:
			rendered.Buttons = renderTemplateButtons(component.Buttons, content)
		}
	}
	return rendered
}

func renderTemplateButtons(buttons []TemplateButton, content TemplateContent) []RenderedButton {
	overrides := quickReplyOverrides(content)
	rendered := make([]RenderedButton, 0, len(buttons))
	for index, button := range buttons {
		if !strings.EqualFold(button.Type, buttonQuickReply) {
			rendered = append(rendered, RenderedButton{Title: button.Text, Kind: ButtonKindLink})
			continue
		}
		// A tap reports the button label as its payload unless the send
		// overrode it for this index.
		payload := button.Text
		if override, ok := overrides[index]; ok {
			payload = override
		}
		rendered = append(rendered, RenderedButton{ID: payload, Title: button.Text, Kind: ButtonKindQuickReply})
	}
	return rendered
}

func quickReplyOverrides(content TemplateContent) map[int]string {
	overrides := make(map[int]string)
	for _, component := range content.Components {
		if !strings.EqualFold(component.Type, "button") || len(component.Parameters) == 0 {
			continue
		}
		index, ok := buttonIndex(component.Index)
		if ok && component.Parameters[0].Payload != "" {
			overrides[index] = component.Parameters[0].Payload
		}
	}
	return overrides
}

// buttonIndex reads an index that Graph accepts as a number or as a string.
func buttonIndex(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		return int(value), true
	case string:
		index, err := strconv.Atoi(value)
		return index, err == nil
	default:
		return 0, false
	}
}

func parametersFor(content TemplateContent, kind string) []ContentParameter {
	for _, component := range content.Components {
		if strings.EqualFold(component.Type, kind) {
			return component.Parameters
		}
	}
	return nil
}

func placeholders(text string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(text, -1)
	names := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, repeated := seen[match[1]]; repeated {
			continue
		}
		seen[match[1]] = struct{}{}
		names = append(names, match[1])
	}
	return names
}

func substitute(text string, parameters []ContentParameter, format string) string {
	return placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := placeholderPattern.FindStringSubmatch(match)[1]
		if format == FormatNamed {
			for _, parameter := range parameters {
				if parameter.ParameterName == name {
					return parameter.value()
				}
			}
			return match
		}
		position, err := strconv.Atoi(name)
		if err != nil || position < 1 || position > len(parameters) {
			return match
		}
		return parameters[position-1].value()
	})
}
