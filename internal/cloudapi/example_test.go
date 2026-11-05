package cloudapi_test

import (
	"fmt"

	"github.com/mvcris/wasim/internal/cloudapi"
)

func ExampleNormalizeRecipient() {
	waID, apiErr := cloudapi.NormalizeRecipient("+1 (555) 000-0001")
	fmt.Println(waID, apiErr == nil)
	// Output: 15550000001 true
}

func ExampleValidateSend() {
	request := cloudapi.SendRequest{
		MessagingProduct: "whatsapp",
		To:               "15550000001",
		Type:             cloudapi.TypeText,
		Text:             &cloudapi.TextContent{},
	}
	apiErr := cloudapi.ValidateSend(request)
	fmt.Println(apiErr.Status(), apiErr.Error())
	fmt.Println(apiErr.Definition.Details)
	// Output:
	// 400 (#131008) Required parameter is missing
	// text.body is required
}

func ExampleValidateTemplateParameters() {
	definition := cloudapi.TemplateDefinition{
		Name:     "order_confirmation",
		Language: "en_US",
		Components: []cloudapi.TemplateComponent{
			{Type: "BODY", Text: "Hi {{1}}, your order {{2}} is confirmed."},
		},
	}
	content := cloudapi.TemplateContent{Components: []cloudapi.ContentComponent{{
		Type:       "body",
		Parameters: []cloudapi.ContentParameter{{Type: "text", Text: "Ana"}},
	}}}
	fmt.Println(cloudapi.ValidateTemplateParameters(definition, content).Definition.Details)
	// Output: body: number of localizable_params (1) does not match the expected number of params (2)
}

func ExampleRenderTemplate() {
	definition := cloudapi.TemplateDefinition{
		Name: "appointment_reminder",
		Components: []cloudapi.TemplateComponent{
			{Type: "BODY", Text: "Hi {{first_name}}, see you on {{date}}."},
			{Type: "BUTTONS", Buttons: []cloudapi.TemplateButton{{Type: "QUICK_REPLY", Text: "Confirm"}}},
		},
	}
	content := cloudapi.TemplateContent{Components: []cloudapi.ContentComponent{{
		Type: "body",
		Parameters: []cloudapi.ContentParameter{
			{Type: "text", ParameterName: "date", Text: "Friday"},
			{Type: "text", ParameterName: "first_name", Text: "Ana"},
		},
	}}}
	rendered := cloudapi.RenderTemplate(definition, content)
	fmt.Println(rendered.Body)
	fmt.Println(rendered.Buttons[0].Title, rendered.Buttons[0].ID)
	// Output:
	// Hi Ana, see you on Friday.
	// Confirm Confirm
}

func ExampleLookupMediaRule() {
	rule, err := cloudapi.LookupMediaRule("image/jpeg")
	fmt.Println(rule.Kind, rule.MaxBytes, err)
	_, err = cloudapi.LookupMediaRule("image/gif")
	fmt.Println(err)
	// Output:
	// image 5242880 <nil>
	// unsupported media type "image/gif"
}
