// Package cloudapi models the wire contract of the WhatsApp Cloud API: request
// shapes, documented limits, error codes and template rules. It performs no I/O
// so the contract can be tested and read on its own.
package cloudapi

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Documented limits, see docs/research/cloud-api-basics.md section 8. Meta
// states them in "characters" without defining the unit, so they are counted as
// Unicode code points.
const (
	maxTextBody          = 4096
	maxButtonCount       = 3
	maxButtonID          = 256
	maxButtonTitle       = 20
	maxButtonBody        = 1024
	maxListBody          = 4096
	maxHeaderText        = 60
	maxFooterText        = 60
	maxListButton        = 20
	maxListSections      = 10
	maxListRows          = 10
	maxListSectionTitle  = 24
	maxListRowID         = 200
	maxListRowTitle      = 24
	maxListRowDesc       = 72
	maxRecipientDigits   = 15
	minRecipientDigits   = 5
	messagingProductName = "whatsapp"
)

// Message types accepted on the send endpoint.
const (
	TypeText        = "text"
	TypeInteractive = "interactive"
	TypeTemplate    = "template"

	interactiveButton = "button"
	interactiveList   = "list"
)

// SendRequest is the body of POST /{version}/{phone-number-id}/messages. Fields
// the emulator does not read are ignored, as real consumers send more than the
// basics.
type SendRequest struct {
	MessagingProduct string           `json:"messaging_product"`
	RecipientType    string           `json:"recipient_type,omitempty"`
	To               string           `json:"to,omitempty"`
	Type             string           `json:"type,omitempty"`
	Context          *SendContext     `json:"context,omitempty"`
	Text             *TextContent     `json:"text,omitempty"`
	Interactive      *Interactive     `json:"interactive,omitempty"`
	Template         *TemplateContent `json:"template,omitempty"`

	// Status and MessageID make the same endpoint act as "mark as read".
	Status    string `json:"status,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// SendContext points a reply at the wamid it quotes.
type SendContext struct {
	MessageID string `json:"message_id"`
}

// TextContent is the text message payload.
type TextContent struct {
	Body       string `json:"body"`
	PreviewURL bool   `json:"preview_url,omitempty"`
}

// Interactive is the payload of reply-button and list messages.
type Interactive struct {
	Type   string            `json:"type"`
	Header *InteractiveHead  `json:"header,omitempty"`
	Body   *InteractiveText  `json:"body,omitempty"`
	Footer *InteractiveText  `json:"footer,omitempty"`
	Action InteractiveAction `json:"action"`
}

// InteractiveHead is the optional header. Only text headers are rendered; media
// headers pass validation and show as a label.
type InteractiveHead struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// InteractiveText is a text-only block.
type InteractiveText struct {
	Text string `json:"text"`
}

// InteractiveAction holds either reply buttons or a list.
type InteractiveAction struct {
	Buttons  []ReplyButton `json:"buttons,omitempty"`
	Button   string        `json:"button,omitempty"`
	Sections []ListSection `json:"sections,omitempty"`
}

// ReplyButton is one reply button.
type ReplyButton struct {
	Type  string `json:"type"`
	Reply struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"reply"`
}

// ListSection groups list rows.
type ListSection struct {
	Title string    `json:"title,omitempty"`
	Rows  []ListRow `json:"rows"`
}

// ListRow is one selectable list row.
type ListRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// IsMarkAsRead reports whether the request marks a message as read rather than
// sending one.
func (r SendRequest) IsMarkAsRead() bool {
	return r.Status == "read"
}

// MessageType returns the effective type. Cloud API treats a missing type as text.
func (r SendRequest) MessageType() string {
	if r.Type == "" {
		return TypeText
	}
	return r.Type
}

// NeedsServiceWindow reports whether the message can only go out inside the 24h
// customer service window. Templates are the one type allowed outside it.
func (r SendRequest) NeedsServiceWindow() bool {
	return r.MessageType() != TypeTemplate
}

// NormalizeRecipient reduces a `to` value to digits. Cloud API accepts plus
// signs, hyphens, parentheses and spaces.
func NormalizeRecipient(to string) (string, *APIError) {
	if strings.TrimSpace(to) == "" {
		return "", NewError(CodeMissingParameter, "to is required")
	}
	var digits strings.Builder
	for _, char := range to {
		switch {
		case char >= '0' && char <= '9':
			digits.WriteRune(char)
		case char == '+' || char == '-' || char == '(' || char == ')' || char == ' ':
		default:
			return "", NewError(CodeInvalidValue, fmt.Sprintf("to contains an invalid character: %q", char))
		}
	}
	waID := digits.String()
	if len(waID) < minRecipientDigits || len(waID) > maxRecipientDigits {
		return "", NewError(CodeInvalidValue, fmt.Sprintf("to must have between %d and %d digits", minRecipientDigits, maxRecipientDigits))
	}
	return waID, nil
}

// ValidateSend checks the envelope and the type-specific payload against the
// documented rules. `recipient_type` stays optional: the reference marks it as
// required, but production consumers omit it and Meta accepts them.
func ValidateSend(request SendRequest) *APIError {
	if request.MessagingProduct != messagingProductName {
		return NewError(CodeInvalidParameter, `messaging_product must be "whatsapp"`)
	}
	if request.RecipientType != "" && request.RecipientType != "individual" {
		return NewError(CodeInvalidValue, `recipient_type must be "individual"`)
	}
	switch request.MessageType() {
	case TypeText:
		return validateText(request.Text)
	case TypeInteractive:
		return validateInteractive(request.Interactive)
	case TypeTemplate:
		return validateTemplateEnvelope(request.Template)
	default:
		return NewError(CodeUnsupportedType, fmt.Sprintf("wasim does not emulate message type %q yet", request.MessageType()))
	}
}

func validateText(text *TextContent) *APIError {
	if text == nil || text.Body == "" {
		return NewError(CodeMissingParameter, "text.body is required")
	}
	return checkLength("text.body", text.Body, maxTextBody)
}

func validateInteractive(interactive *Interactive) *APIError {
	if interactive == nil {
		return NewError(CodeMissingParameter, "interactive is required")
	}
	switch interactive.Type {
	case interactiveButton:
		return validateButtons(interactive)
	case interactiveList:
		return validateList(interactive)
	case "":
		return NewError(CodeMissingParameter, "interactive.type is required")
	default:
		return NewError(CodeUnsupportedType, fmt.Sprintf("wasim does not emulate interactive type %q yet", interactive.Type))
	}
}

func validateButtons(interactive *Interactive) *APIError {
	if err := validateInteractiveFrame(interactive, maxButtonBody); err != nil {
		return err
	}
	buttons := interactive.Action.Buttons
	if len(buttons) == 0 || len(buttons) > maxButtonCount {
		return NewError(CodeInvalidParameter, fmt.Sprintf("interactive.action.buttons must have between 1 and %d items", maxButtonCount))
	}
	seenTitles := make(map[string]struct{}, len(buttons))
	seenIDs := make(map[string]struct{}, len(buttons))
	for index, button := range buttons {
		field := fmt.Sprintf("interactive.action.buttons[%d]", index)
		if button.Type != "reply" {
			return NewError(CodeInvalidValue, field+`.type must be "reply"`)
		}
		if err := checkRequired(field+".reply.id", button.Reply.ID, maxButtonID); err != nil {
			return err
		}
		if err := checkRequired(field+".reply.title", button.Reply.Title, maxButtonTitle); err != nil {
			return err
		}
		if _, taken := seenTitles[button.Reply.Title]; taken {
			return NewError(CodeInvalidParameter, field+".reply.title must be unique within the message")
		}
		if _, taken := seenIDs[button.Reply.ID]; taken {
			return NewError(CodeInvalidParameter, field+".reply.id must be unique within the message")
		}
		seenTitles[button.Reply.Title] = struct{}{}
		seenIDs[button.Reply.ID] = struct{}{}
	}
	return nil
}

func validateList(interactive *Interactive) *APIError {
	if err := validateInteractiveFrame(interactive, maxListBody); err != nil {
		return err
	}
	if interactive.Header != nil && interactive.Header.Type != "text" {
		return NewError(CodeInvalidValue, "interactive.header.type must be text for list messages")
	}
	action := interactive.Action
	if err := checkRequired("interactive.action.button", action.Button, maxListButton); err != nil {
		return err
	}
	if len(action.Sections) == 0 || len(action.Sections) > maxListSections {
		return NewError(CodeInvalidParameter, fmt.Sprintf("interactive.action.sections must have between 1 and %d items", maxListSections))
	}
	rowCount := 0
	seenIDs := make(map[string]struct{})
	for sectionIndex, section := range action.Sections {
		field := fmt.Sprintf("interactive.action.sections[%d]", sectionIndex)
		if section.Title == "" && len(action.Sections) > 1 {
			return NewError(CodeMissingParameter, field+".title is required when there is more than one section")
		}
		if err := checkLength(field+".title", section.Title, maxListSectionTitle); err != nil {
			return err
		}
		for rowIndex, row := range section.Rows {
			rowField := fmt.Sprintf("%s.rows[%d]", field, rowIndex)
			if err := checkRequired(rowField+".id", row.ID, maxListRowID); err != nil {
				return err
			}
			if err := checkRequired(rowField+".title", row.Title, maxListRowTitle); err != nil {
				return err
			}
			if err := checkLength(rowField+".description", row.Description, maxListRowDesc); err != nil {
				return err
			}
			if _, taken := seenIDs[row.ID]; taken {
				return NewError(CodeInvalidParameter, rowField+".id must be unique within the message")
			}
			seenIDs[row.ID] = struct{}{}
		}
		rowCount += len(section.Rows)
	}
	if rowCount == 0 || rowCount > maxListRows {
		return NewError(CodeInvalidParameter, fmt.Sprintf("a list must have between 1 and %d rows across all sections", maxListRows))
	}
	return nil
}

func validateInteractiveFrame(interactive *Interactive, maxBody int) *APIError {
	if interactive.Body == nil {
		return NewError(CodeMissingParameter, "interactive.body.text is required")
	}
	if err := checkRequired("interactive.body.text", interactive.Body.Text, maxBody); err != nil {
		return err
	}
	if interactive.Header != nil && interactive.Header.Type == "text" {
		if err := checkLength("interactive.header.text", interactive.Header.Text, maxHeaderText); err != nil {
			return err
		}
	}
	if interactive.Footer != nil {
		if err := checkLength("interactive.footer.text", interactive.Footer.Text, maxFooterText); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplateEnvelope(template *TemplateContent) *APIError {
	if template == nil {
		return NewError(CodeMissingParameter, "template is required")
	}
	if template.Name == "" {
		return NewError(CodeMissingParameter, "template.name is required")
	}
	if template.Language.Code == "" {
		return NewError(CodeMissingParameter, "template.language.code is required")
	}
	return nil
}

func checkRequired(field, value string, limit int) *APIError {
	if value == "" {
		return NewError(CodeMissingParameter, field+" is required")
	}
	return checkLength(field, value, limit)
}

func checkLength(field, value string, limit int) *APIError {
	if utf8.RuneCountInString(value) > limit {
		return NewError(CodeInvalidParameter, fmt.Sprintf("%s must be at most %d characters", field, limit))
	}
	return nil
}
