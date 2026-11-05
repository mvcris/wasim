package cloudapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEncodeWAMIDMatchesMetaSamples(t *testing.T) {
	tests := []struct {
		name   string
		waID   string
		tag    byte
		key    string
		expect string
	}{
		{
			name:   "inbound sample from the text webhook reference",
			waID:   "16503879439",
			tag:    wamidInboundTag,
			key:    "3A4A6599AEE03810144F",
			expect: "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
		},
		{
			name:   "outbound sample from the interactive webhook reference",
			waID:   "16503879439",
			tag:    wamidOutboundTag,
			key:    "70C6EBE6B4DCFA5CF5",
			expect: "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI3MEM2RUJFNkI0RENGQTVDRjUA",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := encodeWAMID(test.waID, test.tag, test.key); got != test.expect {
				t.Errorf("encodeWAMID() = %q, want %q", got, test.expect)
			}
		})
	}
}

func TestNewWAMIDsAreUnique(t *testing.T) {
	first, second := NewOutboundWAMID("441632960961"), NewOutboundWAMID("441632960961")
	if first == second {
		t.Errorf("two outbound wamids collided: %s", first)
	}
}

func TestNormalizeRecipient(t *testing.T) {
	tests := []struct {
		input    string
		want     string
		wantCode int
	}{
		{input: "+1 (650) 555-1234", want: "16505551234"},
		{input: "441632960961", want: "441632960961"},
		{input: "", wantCode: CodeMissingParameter},
		{input: "abc", wantCode: CodeInvalidValue},
		{input: "123", wantCode: CodeInvalidValue},
		{input: "1234567890123456", wantCode: CodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, apiErr := NormalizeRecipient(test.input)
			assertCode(t, apiErr, test.wantCode)
			if got != test.want {
				t.Errorf("NormalizeRecipient() = %q, want %q", got, test.want)
			}
		})
	}
}

func reply(id, title string) ReplyButton {
	button := ReplyButton{Type: "reply"}
	button.Reply.ID = id
	button.Reply.Title = title
	return button
}

func buttonRequest(buttons ...ReplyButton) SendRequest {
	return SendRequest{
		MessagingProduct: "whatsapp",
		Type:             TypeInteractive,
		Interactive: &Interactive{
			Type:   interactiveButton,
			Body:   &InteractiveText{Text: "Pick one"},
			Action: InteractiveAction{Buttons: buttons},
		},
	}
}

func listRequest(button string, sections ...ListSection) SendRequest {
	return SendRequest{
		MessagingProduct: "whatsapp",
		Type:             TypeInteractive,
		Interactive: &Interactive{
			Type:   interactiveList,
			Body:   &InteractiveText{Text: "Pick one"},
			Action: InteractiveAction{Button: button, Sections: sections},
		},
	}
}

func rows(count int) []ListRow {
	result := make([]ListRow, count)
	for index := range result {
		result[index] = ListRow{ID: "row" + strings.Repeat("x", index), Title: "Row"}
	}
	return result
}

func TestValidateSend(t *testing.T) {
	text := func(body string) SendRequest {
		return SendRequest{MessagingProduct: "whatsapp", Type: TypeText, Text: &TextContent{Body: body}}
	}
	tests := []struct {
		name     string
		request  SendRequest
		wantCode int
	}{
		{name: "text", request: text("hi")},
		{name: "recipient_type may be omitted", request: text("hi")},
		{name: "type defaults to text", request: SendRequest{MessagingProduct: "whatsapp", Text: &TextContent{Body: "hi"}}},
		{name: "text at the limit", request: text(strings.Repeat("a", 4096))},
		{name: "limits count characters, not bytes", request: text(strings.Repeat("é", 4096))},
		{name: "text over the limit", request: text(strings.Repeat("a", 4097)), wantCode: CodeInvalidParameter},
		{name: "text without body", request: SendRequest{MessagingProduct: "whatsapp", Type: TypeText}, wantCode: CodeMissingParameter},
		{name: "wrong product", request: SendRequest{MessagingProduct: "telegram"}, wantCode: CodeInvalidParameter},
		{name: "group recipients are not emulated", request: SendRequest{MessagingProduct: "whatsapp", RecipientType: "group"}, wantCode: CodeInvalidValue},
		{name: "unknown type", request: SendRequest{MessagingProduct: "whatsapp", Type: "sticker"}, wantCode: CodeUnsupportedType},

		{name: "three buttons", request: buttonRequest(reply("a", "A"), reply("b", "B"), reply("c", "C"))},
		{name: "no buttons", request: buttonRequest(), wantCode: CodeInvalidParameter},
		{name: "four buttons", request: buttonRequest(reply("a", "A"), reply("b", "B"), reply("c", "C"), reply("d", "D")), wantCode: CodeInvalidParameter},
		{name: "button title over 20", request: buttonRequest(reply("a", strings.Repeat("t", 21))), wantCode: CodeInvalidParameter},
		{name: "button id over 256", request: buttonRequest(reply(strings.Repeat("i", 257), "A")), wantCode: CodeInvalidParameter},
		{name: "button without id", request: buttonRequest(reply("", "A")), wantCode: CodeMissingParameter},
		{name: "repeated button title", request: buttonRequest(reply("a", "Same"), reply("b", "Same")), wantCode: CodeInvalidParameter},

		{name: "list", request: listRequest("Open", ListSection{Rows: rows(10)})},
		{name: "list without opener", request: listRequest("", ListSection{Rows: rows(1)}), wantCode: CodeMissingParameter},
		{name: "list opener over 20", request: listRequest(strings.Repeat("o", 21), ListSection{Rows: rows(1)}), wantCode: CodeInvalidParameter},
		{name: "eleven rows", request: listRequest("Open", ListSection{Rows: rows(11)}), wantCode: CodeInvalidParameter},
		{name: "row title over 24", request: listRequest("Open", ListSection{Rows: []ListRow{{ID: "a", Title: strings.Repeat("t", 25)}}}), wantCode: CodeInvalidParameter},
		{name: "row description over 72", request: listRequest("Open", ListSection{Rows: []ListRow{{ID: "a", Title: "A", Description: strings.Repeat("d", 73)}}}), wantCode: CodeInvalidParameter},
		{name: "several sections need titles", request: listRequest("Open", ListSection{Rows: rows(1)}, ListSection{Title: "B", Rows: []ListRow{{ID: "b", Title: "B"}}}), wantCode: CodeMissingParameter},

		{name: "template without name", request: SendRequest{MessagingProduct: "whatsapp", Type: TypeTemplate, Template: &TemplateContent{Language: TemplateLanguage{Code: "en_US"}}}, wantCode: CodeMissingParameter},
		{name: "template without language", request: SendRequest{MessagingProduct: "whatsapp", Type: TypeTemplate, Template: &TemplateContent{Name: "x"}}, wantCode: CodeMissingParameter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCode(t, ValidateSend(test.request), test.wantCode)
		})
	}
}

func TestListRowsAreCountedAcrossSections(t *testing.T) {
	first := ListSection{Title: "A", Rows: rows(6)}
	second := ListSection{Title: "B", Rows: []ListRow{
		{ID: "b1", Title: "B"}, {ID: "b2", Title: "B"}, {ID: "b3", Title: "B"}, {ID: "b4", Title: "B"}, {ID: "b5", Title: "B"},
	}}
	assertCode(t, ValidateSend(listRequest("Open", first, second)), CodeInvalidParameter)
}

func positionalTemplate() TemplateDefinition {
	return TemplateDefinition{
		Name:   "order",
		Status: "APPROVED",
		Components: []TemplateComponent{
			{Type: "HEADER", Format: "TEXT", Text: "Order {{1}}"},
			{Type: "BODY", Text: "Hi {{1}}, your order {{2}} is ready. Thanks, {{1}}!"},
			{Type: "FOOTER", Text: "Footer"},
			{Type: "BUTTONS", Buttons: []TemplateButton{
				{Type: "QUICK_REPLY", Text: "Track"},
				{Type: "URL", Text: "Site", URL: "https://example.com"},
				{Type: "QUICK_REPLY", Text: "Stop"},
			}},
		},
	}
}

func namedTemplate() TemplateDefinition {
	return TemplateDefinition{
		Name:   "reminder",
		Status: "APPROVED",
		Components: []TemplateComponent{
			{Type: "BODY", Text: "Hi {{first_name}}, see you on {{date}}."},
		},
	}
}

func textParameters(values ...string) []ContentParameter {
	parameters := make([]ContentParameter, len(values))
	for index, value := range values {
		parameters[index] = ContentParameter{Type: "text", Text: value}
	}
	return parameters
}

func TestValidateTemplateParameters(t *testing.T) {
	validPositional := []ContentComponent{
		{Type: "header", Parameters: textParameters("42")},
		{Type: "body", Parameters: textParameters("Ana", "42")},
	}
	named := func(pairs ...string) []ContentComponent {
		parameters := []ContentParameter{}
		for index := 0; index < len(pairs); index += 2 {
			parameters = append(parameters, ContentParameter{Type: "text", ParameterName: pairs[index], Text: pairs[index+1]})
		}
		return []ContentComponent{{Type: "body", Parameters: parameters}}
	}
	tests := []struct {
		name       string
		definition TemplateDefinition
		components []ContentComponent
		wantCode   int
	}{
		{name: "positional", definition: positionalTemplate(), components: validPositional},
		{name: "component type case does not matter", definition: positionalTemplate(), components: []ContentComponent{
			{Type: "HEADER", Parameters: textParameters("42")},
			{Type: "BODY", Parameters: textParameters("Ana", "42")},
		}},
		{name: "too few values", definition: positionalTemplate(), components: []ContentComponent{
			{Type: "header", Parameters: textParameters("42")},
			{Type: "body", Parameters: textParameters("Ana")},
		}, wantCode: CodeTemplateParamCount},
		{name: "names on a positional template", definition: positionalTemplate(), components: []ContentComponent{
			{Type: "header", Parameters: textParameters("42")},
			{Type: "body", Parameters: []ContentParameter{{Type: "text", ParameterName: "a", Text: "x"}, {Type: "text", ParameterName: "b", Text: "y"}}},
		}, wantCode: CodeTemplateParamCount},
		{name: "empty value", definition: positionalTemplate(), components: []ContentComponent{
			{Type: "header", Parameters: textParameters("42")},
			{Type: "body", Parameters: textParameters("Ana", "")},
		}, wantCode: CodeMissingParameter},
		{name: "named in any order", definition: namedTemplate(), components: named("date", "Monday", "first_name", "Ana")},
		{name: "no names on a named template", definition: namedTemplate(), components: []ContentComponent{
			{Type: "body", Parameters: textParameters("Ana", "Monday")},
		}, wantCode: CodeTemplateParamCount},
		{name: "unknown name", definition: namedTemplate(), components: named("first_name", "Ana", "time", "9am"), wantCode: CodeTemplateParamCount},
		{name: "repeated name", definition: namedTemplate(), components: named("first_name", "Ana", "first_name", "Bia"), wantCode: CodeTemplateParamCount},
		{name: "fixed body with empty components", definition: TemplateDefinition{Components: []TemplateComponent{{Type: "BODY", Text: "Hello"}}}, components: []ContentComponent{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := TemplateContent{Name: "x", Components: test.components}
			assertCode(t, ValidateTemplateParameters(test.definition, content), test.wantCode)
		})
	}
}

func TestParameterCountErrorMatchesMetaWording(t *testing.T) {
	apiErr := ValidateTemplateParameters(namedTemplate(), TemplateContent{})
	want := "body: number of localizable_params (0) does not match the expected number of params (2)"
	if apiErr == nil || apiErr.Definition.Details != want {
		t.Errorf("details = %v, want %q", apiErr, want)
	}
}

func TestTemplateFormatIsInferredWithoutACatalogValue(t *testing.T) {
	if got := positionalTemplate().Format(); got != FormatPositional {
		t.Errorf("positional template Format() = %q", got)
	}
	if got := namedTemplate().Format(); got != FormatNamed {
		t.Errorf("named template Format() = %q", got)
	}
}

func TestRenderTemplate(t *testing.T) {
	content := TemplateContent{Components: []ContentComponent{
		{Type: "header", Parameters: textParameters("42")},
		{Type: "body", Parameters: textParameters("Ana", "42")},
		{Type: "button", SubType: "quick_reply", Index: float64(2), Parameters: []ContentParameter{{Type: "payload", Payload: "stop-all"}}},
	}}
	rendered := RenderTemplate(positionalTemplate(), content)

	if want := "Hi Ana, your order 42 is ready. Thanks, Ana!"; rendered.Body != want {
		t.Errorf("Body = %q, want %q", rendered.Body, want)
	}
	if want := "Order 42"; rendered.Header != want {
		t.Errorf("Header = %q, want %q", rendered.Header, want)
	}
	if rendered.Footer != "Footer" {
		t.Errorf("Footer = %q", rendered.Footer)
	}
	wantButtons := []RenderedButton{
		{ID: "Track", Title: "Track", Kind: ButtonKindQuickReply},
		{Title: "Site", Kind: ButtonKindLink},
		{ID: "stop-all", Title: "Stop", Kind: ButtonKindQuickReply},
	}
	if len(rendered.Buttons) != len(wantButtons) {
		t.Fatalf("Buttons = %+v", rendered.Buttons)
	}
	for index, want := range wantButtons {
		if rendered.Buttons[index] != want {
			t.Errorf("Buttons[%d] = %+v, want %+v", index, rendered.Buttons[index], want)
		}
	}
}

func TestRenderedFindsOnlyTappableOptions(t *testing.T) {
	rendered := RenderInteractive(Interactive{
		Type:   interactiveList,
		Body:   &InteractiveText{Text: "Pick"},
		Action: InteractiveAction{Button: "Open", Sections: []ListSection{{Rows: []ListRow{{ID: "a", Title: "A"}}}}},
	})
	if _, ok := rendered.FindRow("a"); !ok {
		t.Error("FindRow(a) did not find the row")
	}
	if _, ok := rendered.FindRow("b"); ok {
		t.Error("FindRow(b) found a row that does not exist")
	}

	withLink := Rendered{Buttons: []RenderedButton{{ID: "x", Title: "Site", Kind: ButtonKindLink}}}
	if _, ok := withLink.FindButton("x"); ok {
		t.Error("a link button must not be tappable")
	}
}

func TestLookupMediaRule(t *testing.T) {
	tests := []struct {
		mimeType string
		wantKind string
		wantErr  bool
	}{
		{mimeType: "image/jpeg", wantKind: MediaImage},
		{mimeType: "IMAGE/PNG", wantKind: MediaImage},
		{mimeType: "application/pdf", wantKind: MediaDocument},
		{mimeType: "text/plain; charset=utf-8", wantKind: MediaDocument},
		{mimeType: "image/gif", wantErr: true},
		{mimeType: "image/svg+xml", wantErr: true},
		{mimeType: "", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.mimeType, func(t *testing.T) {
			rule, err := LookupMediaRule(test.mimeType)
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, test.wantErr)
			}
			if rule.Kind != test.wantKind {
				t.Errorf("Kind = %q, want %q", rule.Kind, test.wantKind)
			}
		})
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	apiErr := NewError(CodeTemplateParamCount, "body: mismatch")
	encoded, err := json.Marshal(apiErr.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      int    `json:"code"`
			ErrorData struct {
				MessagingProduct string `json:"messaging_product"`
				Details          string `json:"details"`
			} `json:"error_data"`
			TraceID string `json:"fbtrace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(decoded.Error.Message, "(#132000) ") {
		t.Errorf("message = %q", decoded.Error.Message)
	}
	if decoded.Error.Type != "OAuthException" || decoded.Error.Code != 132000 {
		t.Errorf("type/code = %q/%d", decoded.Error.Type, decoded.Error.Code)
	}
	if decoded.Error.ErrorData.Details != "body: mismatch" || decoded.Error.ErrorData.MessagingProduct != "whatsapp" {
		t.Errorf("error_data = %+v", decoded.Error.ErrorData)
	}
	if decoded.Error.TraceID == "" {
		t.Error("fbtrace_id is empty")
	}
	if apiErr.Status() != http.StatusBadRequest {
		t.Errorf("Status() = %d", apiErr.Status())
	}
}

func TestErrorCatalogHasValidDefaults(t *testing.T) {
	for _, definition := range ErrorCatalog() {
		if !definition.DefaultChannel.Valid() {
			t.Errorf("code %d has channel %q", definition.Code, definition.DefaultChannel)
		}
		if definition.Title == "" || definition.HTTPStatus < 400 {
			t.Errorf("code %d is incomplete: %+v", definition.Code, definition)
		}
	}
}

func assertCode(t *testing.T, apiErr *APIError, want int) {
	t.Helper()
	switch {
	case want == 0 && apiErr != nil:
		t.Errorf("unexpected error: %v (%s)", apiErr, apiErr.Definition.Details)
	case want != 0 && apiErr == nil:
		t.Errorf("expected error %d, got none", want)
	case want != 0 && apiErr.Definition.Code != want:
		t.Errorf("code = %d (%s), want %d", apiErr.Definition.Code, apiErr.Definition.Details, want)
	}
}
