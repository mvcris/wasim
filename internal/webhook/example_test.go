package webhook_test

import (
	"fmt"

	"github.com/mvcris/wasim/internal/webhook"
)

func ExampleSign() {
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	fmt.Println(webhook.Sign("test_app_secret", body))
	// Output: sha256=dec6a680679a968e7e0319355b23599cbfc54502c0784668e9618baa0c633756
}

func ExampleEncode() {
	value := map[string]string{"body": "café"}
	escaped, _ := webhook.Encode(value, true)
	raw, _ := webhook.Encode(value, false)
	fmt.Println(string(escaped))
	fmt.Println(string(raw))
	// Output:
	// {"body":"caf\u00e9"}
	// {"body":"café"}
}

func ExampleInboundEnvelope() {
	account := webhook.Account{BusinessAccountID: "200000000000001", PhoneNumberID: "100000000000001", DisplayPhoneNumber: "15550001234"}
	sender := webhook.Contact{Profile: webhook.Profile{Name: "Ana"}, WaID: "15550000001"}
	message := webhook.InboundMessage{
		From: "15550000001", ID: "wamid.example", Timestamp: "1749416383",
		Type: "text", Text: &webhook.Text{Body: "hi"},
	}
	body, _ := webhook.Encode(webhook.InboundEnvelope(account, sender, message), true)
	fmt.Println(string(body))
	// Output: {"object":"whatsapp_business_account","entry":[{"id":"200000000000001","changes":[{"value":{"messaging_product":"whatsapp","metadata":{"display_phone_number":"15550001234","phone_number_id":"100000000000001"},"contacts":[{"profile":{"name":"Ana"},"wa_id":"15550000001"}],"messages":[{"from":"15550000001","id":"wamid.example","timestamp":"1749416383","type":"text","text":{"body":"hi"}}]},"field":"messages"}]}]}
}
