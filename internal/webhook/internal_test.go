package webhook

import (
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/domain"
)

func TestSignature(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	tests := []struct {
		name   string
		secret string
		bad    bool
		want   func(string) bool
	}{
		{name: "signed with the secret", secret: "s", want: func(got string) bool { return got == Sign("s", body) }},
		{name: "unsigned without a secret", secret: "", want: func(got string) bool { return got == "" }},
		{name: "a bad copy does not verify", secret: "s", bad: true, want: func(got string) bool { return got != "" && got != Sign("s", body) }},
		// A consumer that skips verification when the header is missing would
		// otherwise pass the bad signature test without checking anything.
		{name: "a bad copy is signed even without a secret", secret: "", bad: true, want: func(got string) bool { return got != "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &Dispatcher{config: Config{AppSecret: test.secret}}
			got := dispatcher.signature(domain.Delivery{Body: body, BadSignature: test.bad})
			if !test.want(got) {
				t.Errorf("signature = %q", got)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	dispatcher := &Dispatcher{config: Config{RetryBase: time.Second, RetryMax: 10 * time.Second}}
	// attempts counts the attempt that just failed.
	for attempts, want := range map[int]time.Duration{
		1:  0,
		2:  time.Second,
		3:  2 * time.Second,
		4:  4 * time.Second,
		5:  8 * time.Second,
		6:  10 * time.Second,
		40: 10 * time.Second,
	} {
		if got := dispatcher.retryDelay(attempts); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestNewChallengeIsTenDigits(t *testing.T) {
	for range 50 {
		challenge := newChallenge()
		if len(challenge) != 10 || challenge[0] == '0' {
			t.Fatalf("challenge %q is not a ten digit number", challenge)
		}
	}
}

func TestNewPricing(t *testing.T) {
	service := NewPricing("")
	if service.Billable || service.Type != "free_customer_service" || service.Category != "service" || service.PricingModel != "PMP" {
		t.Errorf("service pricing = %+v", service)
	}
	marketing := NewPricing("marketing")
	if !marketing.Billable || marketing.Type != "regular" || marketing.Category != "marketing" {
		t.Errorf("marketing pricing = %+v", marketing)
	}
}
