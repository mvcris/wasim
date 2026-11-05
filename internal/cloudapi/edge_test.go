package cloudapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestValidateInteractiveEdges(t *testing.T) {
	interactive := func(build func(*Interactive)) SendRequest {
		value := &Interactive{
			Type:   "button",
			Body:   &InteractiveText{Text: "Pick"},
			Action: InteractiveAction{Buttons: []ReplyButton{reply("a", "A")}},
		}
		build(value)
		return SendRequest{MessagingProduct: "whatsapp", Type: TypeInteractive, Interactive: value}
	}
	tests := []struct {
		name     string
		request  SendRequest
		wantCode int
	}{
		{name: "interactive object missing", request: SendRequest{MessagingProduct: "whatsapp", Type: TypeInteractive}, wantCode: CodeMissingParameter},
		{name: "interactive type missing", request: interactive(func(i *Interactive) { i.Type = "" }), wantCode: CodeMissingParameter},
		{name: "interactive type not emulated", request: interactive(func(i *Interactive) { i.Type = "flow" }), wantCode: CodeUnsupportedType},
		{name: "body missing", request: interactive(func(i *Interactive) { i.Body = nil }), wantCode: CodeMissingParameter},
		{name: "button body over 1024", request: interactive(func(i *Interactive) { i.Body.Text = strings.Repeat("b", 1025) }), wantCode: CodeInvalidParameter},
		{name: "text header over 60", request: interactive(func(i *Interactive) { i.Header = &InteractiveHead{Type: "text", Text: strings.Repeat("h", 61)} }), wantCode: CodeInvalidParameter},
		{name: "media header is not length checked", request: interactive(func(i *Interactive) { i.Header = &InteractiveHead{Type: "image"} })},
		{name: "footer over 60", request: interactive(func(i *Interactive) { i.Footer = &InteractiveText{Text: strings.Repeat("f", 61)} }), wantCode: CodeInvalidParameter},
		{name: "button type other than reply", request: interactive(func(i *Interactive) { i.Action.Buttons[0].Type = "url" }), wantCode: CodeInvalidValue},
		{name: "repeated button id", request: interactive(func(i *Interactive) { i.Action.Buttons = []ReplyButton{reply("a", "A"), reply("a", "B")} }), wantCode: CodeInvalidParameter},
		{name: "list body may reach 4096", request: listWithBody(strings.Repeat("b", 4096))},
		{name: "list header must be text", request: withListHeader("image"), wantCode: CodeInvalidValue},
		{name: "list text header is fine", request: withListHeader("text")},
		{name: "eleven sections", request: listRequest("Open", manySections(11)...), wantCode: CodeInvalidParameter},
		{name: "section title over 24", request: listRequest("Open", ListSection{Title: strings.Repeat("s", 25), Rows: rows(1)}), wantCode: CodeInvalidParameter},
		{name: "repeated row id across sections", request: listRequest("Open", ListSection{Title: "A", Rows: []ListRow{{ID: "x", Title: "X"}}}, ListSection{Title: "B", Rows: []ListRow{{ID: "x", Title: "Y"}}}), wantCode: CodeInvalidParameter},
		{name: "row without title", request: listRequest("Open", ListSection{Rows: []ListRow{{ID: "x"}}}), wantCode: CodeMissingParameter},
		{name: "row id over 200", request: listRequest("Open", ListSection{Rows: []ListRow{{ID: strings.Repeat("i", 201), Title: "X"}}}), wantCode: CodeInvalidParameter},
		{name: "no rows at all", request: listRequest("Open", ListSection{}), wantCode: CodeInvalidParameter},
		{name: "template object missing", request: SendRequest{MessagingProduct: "whatsapp", Type: TypeTemplate}, wantCode: CodeMissingParameter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCode(t, ValidateSend(test.request), test.wantCode)
		})
	}
}

func listWithBody(body string) SendRequest {
	request := listRequest("Open", ListSection{Rows: rows(1)})
	request.Interactive.Body = &InteractiveText{Text: body}
	return request
}

func withListHeader(kind string) SendRequest {
	request := listRequest("Open", ListSection{Rows: rows(1)})
	request.Interactive.Header = &InteractiveHead{Type: kind, Text: "Header"}
	return request
}

func manySections(count int) []ListSection {
	sections := make([]ListSection, count)
	for index := range sections {
		sections[index] = ListSection{Title: "S" + strings.Repeat("s", index), Rows: []ListRow{{ID: "r" + strings.Repeat("r", index), Title: "R"}}}
	}
	return sections
}

func TestMarkAsReadAndDefaults(t *testing.T) {
	if !(SendRequest{Status: "read"}).IsMarkAsRead() || (SendRequest{Status: "sent"}).IsMarkAsRead() {
		t.Error("IsMarkAsRead misreads the status field")
	}
	if (SendRequest{}).MessageType() != TypeText {
		t.Error("a missing type should mean text")
	}
	if (SendRequest{Type: TypeTemplate}).NeedsServiceWindow() || !(SendRequest{Type: TypeInteractive}).NeedsServiceWindow() {
		t.Error("only templates may skip the service window")
	}
}

func TestAPIErrorTextAndStatus(t *testing.T) {
	apiErr := NewError(CodeTemplateNotAvailable, "")
	if apiErr.Error() != "(#132001) Template name does not exist in the translation" || apiErr.Status() != http.StatusNotFound {
		t.Errorf("error = %q, status %d", apiErr.Error(), apiErr.Status())
	}
	unknown := NewUnknownObjectError("42")
	if !strings.HasPrefix(unknown.Error(), "Unsupported request. Object with ID '42'") {
		t.Errorf("unknown object error = %q", unknown.Error())
	}
}

func TestNewErrorWithAnUncatalogedCode(t *testing.T) {
	apiErr := NewError(424242, "custom details")
	if apiErr.Status() != http.StatusBadRequest || apiErr.Definition.Details != "custom details" || apiErr.Type != "OAuthException" {
		t.Errorf("error = %+v", apiErr)
	}
}

func TestChannelValid(t *testing.T) {
	if !ChannelSync.Valid() || !ChannelAsync.Valid() || Channel("both").Valid() || Channel("").Valid() {
		t.Error("Channel.Valid accepts the wrong values")
	}
}

func TestQuickReplyPayloadOverride(t *testing.T) {
	definition := TemplateDefinition{Name: "t", Components: []TemplateComponent{{
		Type: "BUTTONS",
		Buttons: []TemplateButton{
			{Type: "QUICK_REPLY", Text: "First"},
			{Type: "QUICK_REPLY", Text: "Second"},
			{Type: "QUICK_REPLY", Text: "Third"},
		},
	}}}
	content := TemplateContent{Components: []ContentComponent{
		{Type: "button", SubType: "quick_reply", Index: float64(0), Parameters: []ContentParameter{{Type: "payload", Payload: "first-id"}}},
		{Type: "button", SubType: "quick_reply", Index: "1", Parameters: []ContentParameter{{Type: "payload", Payload: "second-id"}}},
		{Type: "button", SubType: "quick_reply", Index: "not-a-number", Parameters: []ContentParameter{{Type: "payload", Payload: "ignored"}}},
		{Type: "button", SubType: "quick_reply", Index: true, Parameters: []ContentParameter{{Type: "payload", Payload: "ignored"}}},
	}}

	buttons := RenderTemplate(definition, content).Buttons

	for index, want := range []string{"first-id", "second-id", "Third"} {
		if buttons[index].ID != want {
			t.Errorf("button %d payload = %q, want %q", index, buttons[index].ID, want)
		}
	}
}

func TestCurrencyAndDateParametersRenderTheirFallback(t *testing.T) {
	definition := TemplateDefinition{Components: []TemplateComponent{{Type: "BODY", Text: "Total {{1}} due {{2}}"}}}
	content := TemplateContent{Components: []ContentComponent{{Type: "body", Parameters: []ContentParameter{
		{Type: "currency", Currency: &FallbackText{FallbackValue: "$10.00"}},
		{Type: "date_time", DateTime: &FallbackText{FallbackValue: "May 1"}},
	}}}}

	if apiErr := ValidateTemplateParameters(definition, content); apiErr != nil {
		t.Fatal(apiErr)
	}
	if got := RenderTemplate(definition, content).Body; got != "Total $10.00 due May 1" {
		t.Errorf("body = %q", got)
	}
}

func TestMediaHeadersAreNotCountedAsTextParameters(t *testing.T) {
	definition := TemplateDefinition{Components: []TemplateComponent{
		{Type: "HEADER", Format: "IMAGE"},
		{Type: "BODY", Text: "No parameters"},
	}}
	if apiErr := ValidateTemplateParameters(definition, TemplateContent{}); apiErr != nil {
		t.Errorf("unexpected error: %v", apiErr)
	}
}

func TestEmptyParameterValuesAreRefused(t *testing.T) {
	definition := TemplateDefinition{Components: []TemplateComponent{{Type: "BODY", Text: "Hi {{1}}"}}}
	content := TemplateContent{Components: []ContentComponent{{Type: "body", Parameters: []ContentParameter{{Type: "text"}}}}}
	assertCode(t, ValidateTemplateParameters(definition, content), CodeMissingParameter)
}
