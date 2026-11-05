package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const testSecret = "test_app_secret"

func TestSignMatchesConformanceVectors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty entry", `{"object":"whatsapp_business_account","entry":[]}`, "dec6a680679a968e7e0319355b23599cbfc54502c0784668e9618baa0c633756"},
		{"escaped unicode", `{"text":{"body":"caf\u00e9"}}`, "d36eb0f4e8ed7a1458654758c875f8906c154262156b732bbdf52856281cd3fb"},
		{"raw utf-8", `{"text":{"body":"café"}}`, "d0c411924f46299702b190bbe4b5da59c4bb5641ea114e4e11535ad85929c4f0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := webhook.Sign(testSecret, []byte(test.body)); got != "sha256="+test.want {
				t.Errorf("Sign() = %q, want sha256=%s", got, test.want)
			}
		})
	}
}

func TestEncodeEscapesNonASCII(t *testing.T) {
	value := map[string]string{"body": "café 😀 <b>&"}
	tests := []struct {
		name   string
		escape bool
		want   string
	}{
		{"escaped, emoji as a surrogate pair", true, `{"body":"caf\u00e9 \ud83d\ude00 <b>&"}`},
		{"raw", false, `{"body":"café 😀 <b>&"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := webhook.Encode(value, test.escape)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Errorf("Encode() = %s, want %s", got, test.want)
			}
			var decoded map[string]string
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("result is not valid JSON: %v", err)
			}
			if decoded["body"] != value["body"] {
				t.Errorf("round trip = %q", decoded["body"])
			}
		})
	}
}

func TestEnvelopesHaveTheDocumentedShape(t *testing.T) {
	account := webhook.Account{BusinessAccountID: "waba", PhoneNumberID: "phone", DisplayPhoneNumber: "15550001234"}
	sender := webhook.Contact{Profile: webhook.Profile{Name: "Ana"}, WaID: "5511"}
	inbound := webhook.InboundEnvelope(account, sender, webhook.InboundMessage{
		From: "5511", ID: "wamid.x", Timestamp: "1749416383", Type: "text", Text: &webhook.Text{Body: "hi"},
	})
	got, err := webhook.Encode(inbound, true)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"object":"whatsapp_business_account","entry":[{"id":"waba","changes":[{"value":{"messaging_product":"whatsapp","metadata":{"display_phone_number":"15550001234","phone_number_id":"phone"},"contacts":[{"profile":{"name":"Ana"},"wa_id":"5511"}],"messages":[{"from":"5511","id":"wamid.x","timestamp":"1749416383","type":"text","text":{"body":"hi"}}]},"field":"messages"}]}]}`
	if string(got) != want {
		t.Errorf("inbound envelope:\n got %s\nwant %s", got, want)
	}

	failed := webhook.StatusEnvelope(account, webhook.Status{
		ID: "wamid.y", Status: "failed", Timestamp: "1751142888", RecipientID: "5511",
		Errors: []webhook.StatusError{webhook.NewStatusError(131049, "Title", "Details")},
	})
	got, err = webhook.Encode(failed, true)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"object":"whatsapp_business_account","entry":[{"id":"waba","changes":[{"value":{"messaging_product":"whatsapp","metadata":{"display_phone_number":"15550001234","phone_number_id":"phone"},"statuses":[{"id":"wamid.y","status":"failed","timestamp":"1751142888","recipient_id":"5511","errors":[{"code":131049,"title":"Title","message":"Title","error_data":{"details":"Details"},"href":"/documentation/business-messaging/whatsapp/support/error-codes"}]}]},"field":"messages"}]}]}`
	if string(got) != want {
		t.Errorf("failed status envelope:\n got %s\nwant %s", got, want)
	}
}

func TestVerifyEndpoint(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Query().Get("hub.challenge"))
	}
	tests := []struct {
		name         string
		handler      http.HandlerFunc
		wantVerified bool
	}{
		{"echoes the challenge", echo, true},
		{"wrong body", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "nope") }, false},
		{"rejects the token", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			if got := webhook.VerifyEndpoint(context.Background(), server.URL, "token"); got.Verified != test.wantVerified {
				t.Errorf("Verified = %v (%+v)", got.Verified, got)
			}
		})
	}
}

func TestVerifyEndpointSendsTheDocumentedQuery(t *testing.T) {
	var query map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
	}))
	defer server.Close()

	webhook.VerifyEndpoint(context.Background(), server.URL+"?keep=1", "secret-token")

	if query["hub.mode"][0] != "subscribe" || query["hub.verify_token"][0] != "secret-token" || query["keep"][0] != "1" {
		t.Errorf("query = %v", query)
	}
	if query["hub.challenge"][0] == "" {
		t.Error("hub.challenge is empty")
	}
}

type dispatcherFixture struct {
	store  *store.Store
	cancel context.CancelFunc
	done   chan struct{}
	wake   func()
}

func startDispatcher(t *testing.T, url string, attempts int) *dispatcherFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	database, err := store.Open(ctx, store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := webhook.NewDispatcher(database, webhook.Config{
		URL:         url,
		AppSecret:   testSecret,
		Concurrency: 2,
		MaxAttempts: attempts,
		Timeout:     2 * time.Second,
		RetryBase:   time.Millisecond,
		RetryMax:    5 * time.Millisecond,
	}, slog.New(slog.DiscardHandler), func() {})
	fixture := &dispatcherFixture{store: database, cancel: cancel, done: make(chan struct{}), wake: dispatcher.Wake}
	go func() {
		dispatcher.Run(ctx)
		close(fixture.done)
	}()
	t.Cleanup(func() {
		cancel()
		<-fixture.done
		_ = database.Close()
	})
	return fixture
}

func (f *dispatcherFixture) enqueue(t *testing.T, delivery domain.Delivery) {
	t.Helper()
	now := time.Now()
	delivery.Kind = domain.DeliveryKindMessage
	if delivery.NextAttemptAt.IsZero() {
		delivery.NextAttemptAt = now
	}
	delivery.CreatedAt = now
	if err := f.store.EnqueueDelivery(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	f.wake()
}

func (f *dispatcherFixture) waitFor(t *testing.T, state domain.DeliveryState) domain.Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := f.store.ListDeliveries(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) == 1 && deliveries[0].State == state {
			return deliveries[0]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("delivery never reached state %q", state)
	return domain.Delivery{}
}

func TestDispatcherSendsSignedBytes(t *testing.T) {
	var mu sync.Mutex
	var gotBody []byte
	var gotSignature, gotType string
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		gotBody, gotSignature, gotType = body, r.Header.Get("X-Hub-Signature-256"), r.Header.Get("Content-Type")
	}))
	defer server.Close()
	fixture := startDispatcher(t, server.URL, 3)

	body := []byte(`{"text":{"body":"caf\u00e9"}}`)
	fixture.enqueue(t, domain.Delivery{Body: body})
	delivered := fixture.waitFor(t, domain.DeliveryDelivered)

	mu.Lock()
	defer mu.Unlock()
	if string(gotBody) != string(body) {
		t.Errorf("body = %s", gotBody)
	}
	if gotSignature != webhook.Sign(testSecret, body) {
		t.Errorf("signature = %q", gotSignature)
	}
	if gotType != "application/json" {
		t.Errorf("content type = %q", gotType)
	}
	if delivered.Attempts != 1 || delivered.LastStatus != http.StatusOK || delivered.FinishedAt == nil {
		t.Errorf("delivery = %+v", delivered)
	}
}

func TestDispatcherOnlyTreats200AsAcknowledged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	fixture := startDispatcher(t, server.URL, 3)

	fixture.enqueue(t, domain.Delivery{Body: []byte(`{}`)})
	failed := fixture.waitFor(t, domain.DeliveryFailed)

	if calls.Load() != 3 {
		t.Errorf("attempts reaching the consumer = %d, want 3", calls.Load())
	}
	if failed.LastStatus != http.StatusNoContent || failed.LastError == "" {
		t.Errorf("delivery = %+v", failed)
	}
}

func TestDispatcherRetriesUntilAcknowledged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	fixture := startDispatcher(t, server.URL, 8)

	fixture.enqueue(t, domain.Delivery{Body: []byte(`{}`)})
	delivered := fixture.waitFor(t, domain.DeliveryDelivered)

	if delivered.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", delivered.Attempts)
	}
	if delivered.LastError != "" {
		t.Errorf("LastError = %q, want it cleared", delivered.LastError)
	}
}

func TestDispatcherCanSendABadSignature(t *testing.T) {
	var signature atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		signature.Store(r.Header.Get("X-Hub-Signature-256"))
	}))
	defer server.Close()
	fixture := startDispatcher(t, server.URL, 1)

	body := []byte(`{}`)
	fixture.enqueue(t, domain.Delivery{Body: body, BadSignature: true})
	fixture.waitFor(t, domain.DeliveryDelivered)

	if signature.Load() == webhook.Sign(testSecret, body) {
		t.Error("the signature should not verify")
	}
}

func TestDispatcherHoldsWebhooksUntilTheyAreDue(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	fixture := startDispatcher(t, server.URL, 1)

	fixture.enqueue(t, domain.Delivery{Body: []byte(`{}`), NextAttemptAt: time.Now().Add(time.Hour)})
	time.Sleep(100 * time.Millisecond)

	if calls.Load() != 0 {
		t.Errorf("a future webhook was sent %d times", calls.Load())
	}
}

func TestDispatcherSkipsWithoutAWebhookURL(t *testing.T) {
	fixture := startDispatcher(t, "", 1)
	fixture.enqueue(t, domain.Delivery{Body: []byte(`{}`)})
	fixture.waitFor(t, domain.DeliverySkipped)
}
