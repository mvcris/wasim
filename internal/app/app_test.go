package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/app"
	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/config"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const (
	phoneNumberID = "100000000000001"
	businessID    = "200000000000001"
	displayNumber = "15550001234"
	appSecret     = "secret-for-tests"
	userNumber    = "15550100777"
)

// consumer is a fake app under test: it verifies signatures against raw bytes
// and records every webhook, the way a real integration behaves.
type consumer struct {
	t       *testing.T
	mu      sync.Mutex
	bodies  [][]byte
	status  int
	onEvent func(webhook.Envelope)
}

func (c *consumer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		c.t.Errorf("read webhook: %v", err)
	}
	if r.Header.Get("X-Hub-Signature-256") != webhook.Sign(appSecret, body) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var envelope webhook.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		c.t.Errorf("webhook is not valid JSON: %v", err)
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	status, onEvent := c.status, c.onEvent
	c.mu.Unlock()
	if onEvent != nil {
		onEvent(envelope)
	}
	if status != 0 {
		w.WriteHeader(status)
	}
}

func (c *consumer) firstBody() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.bodies[0])
}

func (c *consumer) configure(status int, onEvent func(webhook.Envelope)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status, c.onEvent = status, onEvent
}

func (c *consumer) values() []webhook.Value {
	c.mu.Lock()
	defer c.mu.Unlock()
	values := make([]webhook.Value, 0, len(c.bodies))
	for _, body := range c.bodies {
		var envelope webhook.Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			c.t.Fatal(err)
		}
		values = append(values, envelope.Entry[0].Changes[0].Value)
	}
	return values
}

type harness struct {
	t        *testing.T
	app      *app.App
	graph    *httptest.Server
	console  *httptest.Server
	consumer *consumer
}

func newHarness(t *testing.T, adjust func(*config.Config)) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	fake := &consumer{t: t}
	callback := httptest.NewServer(fake)
	graph := httptest.NewUnstartedServer(nil)

	cfg, err := config.Load(nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DatabasePath = store.MemoryPath
	cfg.MediaDir = t.TempDir()
	cfg.PublicURL = "http://" + graph.Listener.Addr().String()
	cfg.WebhookURL = callback.URL
	cfg.AppSecret = appSecret
	cfg.StatusDelay = time.Millisecond
	cfg.WebhookRetryBase = time.Millisecond
	cfg.WebhookRetryLimit = 5 * time.Millisecond
	cfg.WebhookAttempts = 3
	if adjust != nil {
		adjust(&cfg)
	}

	application, err := app.New(ctx, cfg, "test", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	graph.Config.Handler = application.GraphHandler
	graph.Start()
	console := httptest.NewServer(application.ConsoleHandler)

	done := make(chan struct{})
	go func() {
		application.RunDispatcher(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		console.Close()
		graph.Close()
		callback.Close()
		_ = application.Close()
	})
	return &harness{t: t, app: application, graph: graph, console: console, consumer: fake}
}

type response struct {
	status int
	body   []byte
	header http.Header
}

func (r response) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.body, target); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (h *harness) do(method, target string, body []byte, headers map[string]string) response {
	h.t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer result.Body.Close()
	content, err := io.ReadAll(result.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return response{status: result.StatusCode, body: content, header: result.Header}
}

var authHeader = map[string]string{"Authorization": "Bearer any-token", "Content-Type": "application/json"}

func (h *harness) send(payload string) response {
	h.t.Helper()
	return h.do(http.MethodPost, h.graph.URL+"/v20.0/"+phoneNumberID+"/messages", []byte(payload), authHeader)
}

func (h *harness) sendText(to, body string) response {
	h.t.Helper()
	return h.send(fmt.Sprintf(`{"messaging_product":"whatsapp","to":%q,"type":"text","text":{"body":%q}}`, to, body))
}

func (h *harness) consolePost(path string, payload any) response {
	h.t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.do(http.MethodPost, h.console.URL+path, encoded, map[string]string{"Content-Type": "application/json"})
}

func (h *harness) userSays(text string) domain.Message {
	h.t.Helper()
	result := h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{"type": "text", "text": text})
	if result.status != http.StatusCreated {
		h.t.Fatalf("user message: HTTP %d: %s", result.status, result.body)
	}
	var message domain.Message
	result.decode(h.t, &message)
	return message
}

func (h *harness) waitForWebhook(match func(webhook.Value) bool) webhook.Value {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, value := range h.consumer.values() {
			if match(value) {
				return value
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("expected webhook never arrived, got %+v", h.consumer.values())
	return webhook.Value{}
}

func statusIs(id, status string) func(webhook.Value) bool {
	return func(value webhook.Value) bool {
		return len(value.Statuses) == 1 && value.Statuses[0].ID == id && value.Statuses[0].Status == status
	}
}

func messageIs(id string) func(webhook.Value) bool {
	return func(value webhook.Value) bool {
		return len(value.Messages) == 1 && value.Messages[0].ID == id
	}
}

type sendResult struct {
	MessagingProduct string `json:"messaging_product"`
	Contacts         []struct {
		Input string `json:"input"`
		WaID  string `json:"wa_id"`
	} `json:"contacts"`
	Messages []struct {
		ID            string `json:"id"`
		MessageStatus string `json:"message_status"`
	} `json:"messages"`
}

type graphError struct {
	Error struct {
		Message   string `json:"message"`
		Type      string `json:"type"`
		Code      int    `json:"code"`
		ErrorData struct {
			Details string `json:"details"`
		} `json:"error_data"`
	} `json:"error"`
}

func (h *harness) expectGraphError(result response, status, code int) graphError {
	h.t.Helper()
	if result.status != status {
		h.t.Fatalf("HTTP %d, want %d: %s", result.status, status, result.body)
	}
	var decoded graphError
	result.decode(h.t, &decoded)
	if decoded.Error.Code != code {
		h.t.Fatalf("code %d, want %d: %s", decoded.Error.Code, code, result.body)
	}
	return decoded
}

func (h *harness) expectSent(result response) string {
	h.t.Helper()
	if result.status != http.StatusOK {
		h.t.Fatalf("send: HTTP %d: %s", result.status, result.body)
	}
	var decoded sendResult
	result.decode(h.t, &decoded)
	return decoded.Messages[0].ID
}

func TestTextRoundTrip(t *testing.T) {
	h := newHarness(t, nil)

	inbound := h.userSays("Is the café open? 😀")
	value := h.waitForWebhook(messageIs(inbound.ID))
	got := value.Messages[0]
	if got.Text == nil || got.Text.Body != "Is the café open? 😀" {
		t.Errorf("text = %+v", got.Text)
	}
	if got.From != userNumber || got.Type != "text" || got.Timestamp == "" {
		t.Errorf("message = %+v", got)
	}
	if len(value.Contacts) != 1 || value.Contacts[0].WaID != userNumber || value.Contacts[0].Profile.Name == "" {
		t.Errorf("contacts = %+v", value.Contacts)
	}
	if value.Metadata.PhoneNumberID != phoneNumberID || value.Metadata.DisplayPhoneNumber != displayNumber {
		t.Errorf("metadata = %+v", value.Metadata)
	}

	result := h.sendText("+"+userNumber, "Received")
	if result.status != http.StatusOK {
		t.Fatalf("send: HTTP %d: %s", result.status, result.body)
	}
	var decoded sendResult
	result.decode(t, &decoded)
	if decoded.MessagingProduct != "whatsapp" || decoded.Contacts[0].Input != "+"+userNumber || decoded.Contacts[0].WaID != userNumber {
		t.Errorf("response = %s", result.body)
	}
	id := decoded.Messages[0].ID
	if !strings.HasPrefix(id, "wamid.") {
		t.Errorf("id = %q", id)
	}
	sent := h.waitForWebhook(statusIs(id, "sent")).Statuses[0]
	if sent.RecipientID != userNumber || sent.Pricing == nil || sent.Pricing.Category != "service" {
		t.Errorf("sent status = %+v", sent)
	}
	h.waitForWebhook(statusIs(id, "delivered"))
}

func TestWebhookBodiesEscapeNonASCIIByDefault(t *testing.T) {
	h := newHarness(t, nil)
	h.waitForWebhook(messageIs(h.userSays("caf\xc3\xa9").ID))
	if body := h.consumer.firstBody(); !strings.Contains(body, `caf\u00e9`) {
		t.Errorf("body = %s", body)
	}
}

func TestWebhookBodiesCanKeepRawUTF8(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.EscapeUnicode = false })
	h.waitForWebhook(messageIs(h.userSays("caf\xc3\xa9").ID))
	if body := h.consumer.firstBody(); !strings.Contains(body, "caf\xc3\xa9") {
		t.Errorf("body = %s", body)
	}
}

func TestAcceptsAnyGraphVersion(t *testing.T) {
	h := newHarness(t, nil)
	for _, version := range []string{"v20.0", "v27.0", "v99.9"} {
		target := h.graph.URL + "/" + version + "/" + businessID + "/message_templates"
		if result := h.do(http.MethodGet, target, nil, authHeader); result.status != http.StatusOK {
			t.Errorf("%s: HTTP %d", version, result.status)
		}
	}
	result := h.do(http.MethodGet, h.graph.URL+"/latest/"+businessID+"/message_templates", nil, authHeader)
	h.expectGraphError(result, http.StatusBadRequest, cloudapi.CodeInvalidParameter)
}

func TestAuthentication(t *testing.T) {
	url := func(h *harness) string { return h.graph.URL + "/v20.0/" + businessID + "/message_templates" }

	t.Run("missing token", func(t *testing.T) {
		h := newHarness(t, nil)
		result := h.do(http.MethodGet, url(h), nil, nil)
		h.expectGraphError(result, http.StatusUnauthorized, cloudapi.CodeAccessToken)
	})
	t.Run("any token is accepted by default", func(t *testing.T) {
		h := newHarness(t, nil)
		if result := h.do(http.MethodGet, url(h), nil, authHeader); result.status != http.StatusOK {
			t.Errorf("HTTP %d", result.status)
		}
	})
	t.Run("configured tokens are enforced", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.AccessTokens = []string{"right"} })
		wrong := h.do(http.MethodGet, url(h), nil, authHeader)
		h.expectGraphError(wrong, http.StatusUnauthorized, cloudapi.CodeAccessToken)
		right := h.do(http.MethodGet, url(h), nil, map[string]string{"Authorization": "Bearer right"})
		if right.status != http.StatusOK {
			t.Errorf("HTTP %d", right.status)
		}
	})
}

func TestUnknownObjectsAnswerGraphErrors(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		name, method, path string
	}{
		{"other phone number", http.MethodPost, "/v20.0/999/messages"},
		{"other business account", http.MethodGet, "/v20.0/999/message_templates"},
		{"unknown media", http.MethodGet, "/v20.0/999"},
		{"unknown route", http.MethodGet, "/v20.0/999/nothing/here"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := h.do(test.method, h.graph.URL+test.path, []byte(`{"messaging_product":"whatsapp"}`), authHeader)
			decoded := h.expectGraphError(result, http.StatusBadRequest, cloudapi.CodeInvalidParameter)
			if decoded.Error.Type != "GraphMethodException" {
				t.Errorf("type = %q", decoded.Error.Type)
			}
		})
	}
}

func TestSendRejectsWhatMetaRejects(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		name    string
		payload string
		status  int
		code    int
	}{
		{"invalid JSON", `{`, 400, cloudapi.CodeInvalidParameter},
		{"missing recipient", `{"messaging_product":"whatsapp","type":"text","text":{"body":"x"}}`, 400, cloudapi.CodeMissingParameter},
		{"sender is recipient", `{"messaging_product":"whatsapp","to":"` + displayNumber + `","type":"text","text":{"body":"x"}}`, 400, cloudapi.CodeSenderIsRecipient},
		{"four buttons", `{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"interactive","interactive":{"type":"button","body":{"text":"x"},"action":{"buttons":[
			{"type":"reply","reply":{"id":"a","title":"A"}},{"type":"reply","reply":{"id":"b","title":"B"}},
			{"type":"reply","reply":{"id":"c","title":"C"}},{"type":"reply","reply":{"id":"d","title":"D"}}]}}}`, 400, cloudapi.CodeInvalidParameter},
		{"unknown template", `{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"nope","language":{"code":"en_US"}}}`, 404, cloudapi.CodeTemplateNotAvailable},
		{"template in another language", `{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"hello_world","language":{"code":"pt_BR"}}}`, 404, cloudapi.CodeTemplateNotAvailable},
		{"template parameter count", `{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"order_confirmation","language":{"code":"en_US"},"components":[{"type":"body","parameters":[{"type":"text","text":"one"}]}]}}`, 400, cloudapi.CodeTemplateParamCount},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h.expectGraphError(h.send(test.payload), test.status, test.code)
		})
	}
}

func TestServiceWindow(t *testing.T) {
	t.Run("a free-form message outside the window fails by webhook", func(t *testing.T) {
		h := newHarness(t, nil)
		id := h.expectSent(h.sendText(userNumber, "Are you there?"))
		failed := h.waitForWebhook(statusIs(id, "failed")).Statuses[0]
		if len(failed.Errors) != 1 || failed.Errors[0].Code != cloudapi.CodeOutsideWindow {
			t.Errorf("errors = %+v", failed.Errors)
		}
	})
	t.Run("the channel can be synchronous", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.WindowErrorChannel = cloudapi.ChannelSync })
		h.expectGraphError(h.sendText(userNumber, "Are you there?"), http.StatusBadRequest, cloudapi.CodeOutsideWindow)
	})
	t.Run("a user message opens the window", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		id := h.expectSent(h.sendText(userNumber, "hello"))
		h.waitForWebhook(statusIs(id, "delivered"))
	})
	t.Run("a template goes out with the window closed", func(t *testing.T) {
		h := newHarness(t, nil)
		id := h.expectSent(h.send(`{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"hello_world","language":{"code":"en_US"},"components":[]}}`))
		h.waitForWebhook(statusIs(id, "delivered"))
	})
	t.Run("expiring closes an open window", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		if result := h.consolePost("/api/contacts/"+userNumber+"/window/expire", map[string]any{}); result.status != http.StatusNoContent {
			t.Fatalf("expire: HTTP %d", result.status)
		}
		id := h.expectSent(h.sendText(userNumber, "late reply"))
		h.waitForWebhook(statusIs(id, "failed"))
	})
	t.Run("a back-dated message leaves the window closed", func(t *testing.T) {
		h := newHarness(t, nil)
		h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{"type": "text", "text": "old", "hours_ago": 25})
		id := h.expectSent(h.sendText(userNumber, "late reply"))
		h.waitForWebhook(statusIs(id, "failed"))
	})
	t.Run("the clock closes the window after 24 hours", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		h.app.Service.SetClock(func() time.Time { return time.Now().Add(25 * time.Hour) })
		id := h.expectSent(h.sendText(userNumber, "late reply"))
		h.waitForWebhookWithAdvancedClock(id)
	})
	t.Run("enforcement can be turned off", func(t *testing.T) {
		h := newHarness(t, func(c *config.Config) { c.EnforceWindow = false })
		id := h.expectSent(h.sendText(userNumber, "hello"))
		h.waitForWebhook(statusIs(id, "delivered"))
	})
}

// waitForWebhookWithAdvancedClock checks the row directly: with the service's
// clock moved forward, the status is scheduled in what the dispatcher's real
// clock still considers the future.
func (h *harness) waitForWebhookWithAdvancedClock(id string) {
	h.t.Helper()
	deliveries, err := h.app.Service.ListDeliveries(context.Background(), 10)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, delivery := range deliveries {
		if delivery.MessageID == id && delivery.MessageStatus == domain.StatusFailed && delivery.ErrorCode == cloudapi.CodeOutsideWindow {
			return
		}
	}
	h.t.Fatalf("no 131047 failure scheduled for %s: %+v", id, deliveries)
}

func TestFailureInjection(t *testing.T) {
	addRule := func(h *harness, code int, channel string, times int) {
		h.t.Helper()
		result := h.consolePost("/api/failure-rules", map[string]any{
			"contact_wa_id": userNumber, "code": code, "channel": channel, "times": times,
		})
		if result.status != http.StatusCreated {
			h.t.Fatalf("add rule: HTTP %d: %s", result.status, result.body)
		}
	}

	t.Run("async uses the webhook once, then sends recover", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		addRule(h, cloudapi.CodeEcosystemEngagement, "async", 1)

		id := h.expectSent(h.sendText(userNumber, "first"))
		failed := h.waitForWebhook(statusIs(id, "failed")).Statuses[0]
		if failed.Errors[0].Code != cloudapi.CodeEcosystemEngagement || failed.Errors[0].ErrorData.Details == "" {
			t.Errorf("errors = %+v", failed.Errors)
		}

		next := h.expectSent(h.sendText(userNumber, "second"))
		h.waitForWebhook(statusIs(next, "delivered"))
	})
	t.Run("sync fails the request and consumes the rule", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		addRule(h, cloudapi.CodeThroughput, "sync", 1)
		h.expectGraphError(h.sendText(userNumber, "first"), http.StatusBadRequest, cloudapi.CodeThroughput)
		h.expectSent(h.sendText(userNumber, "second"))
	})
	t.Run("a code's default channel applies", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		addRule(h, cloudapi.CodeExperiment, "", 1)
		id := h.expectSent(h.sendText(userNumber, "first"))
		failed := h.waitForWebhook(statusIs(id, "failed")).Statuses[0]
		if failed.Errors[0].Code != cloudapi.CodeExperiment {
			t.Errorf("errors = %+v", failed.Errors)
		}
	})
	t.Run("sticky rules keep failing until deleted", func(t *testing.T) {
		h := newHarness(t, nil)
		h.userSays("hi")
		addRule(h, cloudapi.CodePairRateLimit, "sync", domain.RuleSticky)
		h.expectGraphError(h.sendText(userNumber, "a"), http.StatusBadRequest, cloudapi.CodePairRateLimit)
		h.expectGraphError(h.sendText(userNumber, "b"), http.StatusBadRequest, cloudapi.CodePairRateLimit)

		var rules []domain.FailureRule
		h.do(http.MethodGet, h.console.URL+"/api/failure-rules", nil, nil).decode(t, &rules)
		deleted := h.do(http.MethodDelete, fmt.Sprintf("%s/api/failure-rules/%d", h.console.URL, rules[0].ID), nil, nil)
		if deleted.status != http.StatusNoContent {
			t.Fatalf("delete: HTTP %d", deleted.status)
		}
		h.expectSent(h.sendText(userNumber, "c"))
	})
	t.Run("an unknown code is refused", func(t *testing.T) {
		h := newHarness(t, nil)
		result := h.consolePost("/api/failure-rules", map[string]any{"code": 424242})
		if result.status != http.StatusBadRequest {
			t.Errorf("HTTP %d: %s", result.status, result.body)
		}
	})
}

func TestManualStatusWebhooks(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AutoStatuses = nil })
	h.userSays("hi")
	id := h.expectSent(h.sendText(userNumber, "hello"))
	path := "/api/messages/" + url.PathEscape(id) + "/status"

	if result := h.consolePost(path, map[string]any{"status": "read"}); result.status != http.StatusAccepted {
		t.Fatalf("read: HTTP %d: %s", result.status, result.body)
	}
	read := h.waitForWebhook(statusIs(id, "read")).Statuses[0]
	if read.Pricing != nil {
		t.Errorf("read must not repeat pricing already on delivered: %+v", read.Pricing)
	}

	if result := h.consolePost(path, map[string]any{"status": "failed", "code": cloudapi.CodeUndeliverable}); result.status != http.StatusAccepted {
		t.Fatalf("failed: HTTP %d: %s", result.status, result.body)
	}
	h.waitForWebhook(statusIs(id, "failed"))

	if result := h.consolePost(path, map[string]any{"status": "bogus"}); result.status != http.StatusBadRequest {
		t.Errorf("bogus status: HTTP %d", result.status)
	}
}

func interactivePayload(interactive string) string {
	return `{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"interactive","interactive":` + interactive + `}`
}

func TestInteractiveReplies(t *testing.T) {
	h := newHarness(t, nil)
	h.userSays("hi")
	reply := func(contextID, replyID string) webhook.InboundMessage {
		t.Helper()
		result := h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{
			"type": "reply", "context_id": contextID, "reply_id": replyID,
		})
		if result.status != http.StatusCreated {
			t.Fatalf("reply: HTTP %d: %s", result.status, result.body)
		}
		var message domain.Message
		result.decode(t, &message)
		return h.waitForWebhook(messageIs(message.ID)).Messages[0]
	}

	t.Run("button_reply", func(t *testing.T) {
		id := h.expectSent(h.send(interactivePayload(`{"type":"button","body":{"text":"Pick"},"action":{"buttons":[{"type":"reply","reply":{"id":"yes-id","title":"Yes"}}]}}`)))
		got := reply(id, "yes-id")
		if got.Type != "interactive" || got.Interactive.Type != "button_reply" {
			t.Errorf("message = %+v", got)
		}
		if got.Interactive.ButtonReply.ID != "yes-id" || got.Interactive.ButtonReply.Title != "Yes" {
			t.Errorf("button_reply = %+v", got.Interactive.ButtonReply)
		}
		if got.Context == nil || got.Context.ID != id || got.Context.From != displayNumber {
			t.Errorf("context = %+v", got.Context)
		}
	})
	t.Run("list_reply", func(t *testing.T) {
		id := h.expectSent(h.send(interactivePayload(`{"type":"list","body":{"text":"Pick"},"action":{"button":"Open","sections":[{"rows":[{"id":"row-1","title":"One","description":"First"}]}]}}`)))
		got := reply(id, "row-1")
		if got.Interactive.Type != "list_reply" || got.Interactive.ListReply.Description != "First" {
			t.Errorf("message = %+v", got.Interactive)
		}
	})
	t.Run("template quick reply reports the label by default", func(t *testing.T) {
		id := h.expectSent(h.send(`{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"order_confirmation","language":{"code":"en_US"},"components":[{"type":"body","parameters":[{"type":"text","text":"Ana"},{"type":"text","text":"42"}]}]}}`))
		got := reply(id, "Track order")
		if got.Type != "button" || got.Button.Payload != "Track order" || got.Button.Text != "Track order" {
			t.Errorf("message = %+v", got)
		}
	})
	t.Run("named template", func(t *testing.T) {
		result := h.send(`{"messaging_product":"whatsapp","to":"` + userNumber + `","type":"template","template":{"name":"appointment_reminder","language":{"code":"en_US"},"components":[{"type":"body","parameters":[{"type":"text","parameter_name":"date","text":"Monday"},{"type":"text","parameter_name":"first_name","text":"Ana"}]}]}}`)
		id := h.expectSent(result)
		var decoded sendResult
		result.decode(t, &decoded)
		if decoded.Messages[0].MessageStatus != "accepted" {
			t.Errorf("message_status = %q", decoded.Messages[0].MessageStatus)
		}
		status := h.waitForWebhook(statusIs(id, "sent")).Statuses[0]
		if status.Pricing.Category != "marketing" || !status.Pricing.Billable {
			t.Errorf("pricing = %+v", status.Pricing)
		}
		var messages []domain.Message
		h.do(http.MethodGet, h.console.URL+"/api/contacts/"+userNumber+"/messages", nil, nil).decode(t, &messages)
		rendered := messages[len(messages)-1].Rendered
		if rendered.Body != "Hi Ana, your appointment is on Monday." {
			t.Errorf("rendered = %+v", rendered)
		}
	})
	t.Run("an option that does not exist is refused", func(t *testing.T) {
		id := h.expectSent(h.sendText(userNumber, "plain text"))
		result := h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{"type": "reply", "context_id": id, "reply_id": "nope"})
		if result.status != http.StatusBadRequest {
			t.Errorf("HTTP %d: %s", result.status, result.body)
		}
	})
}

func TestQuotedTextReply(t *testing.T) {
	h := newHarness(t, nil)
	first := h.userSays("first")
	id := h.expectSent(h.sendText(userNumber, "hello"))

	quoteOf := func(contextID string) webhook.InboundMessage {
		t.Helper()
		result := h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{"type": "text", "text": "yes, that", "context_id": contextID})
		var message domain.Message
		result.decode(t, &message)
		return h.waitForWebhook(messageIs(message.ID)).Messages[0]
	}
	if got := quoteOf(id); got.Context.ID != id || got.Context.From != displayNumber {
		t.Errorf("quoting the business: %+v", got.Context)
	}
	if got := quoteOf(first.ID); got.Context.ID != first.ID || got.Context.From != userNumber {
		t.Errorf("quoting oneself: %+v", got.Context)
	}
	if result := h.consolePost("/api/contacts/"+userNumber+"/messages", map[string]any{"type": "text", "text": "x", "context_id": "wamid.none"}); result.status != http.StatusBadRequest {
		t.Errorf("unknown quote: HTTP %d", result.status)
	}
}

// uploadFile posts a file as the simulated user and returns its webhook.
func (h *harness) uploadFile(filename, mimeType, caption string, content []byte) response {
	h.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", mimeType)
	part, err := form.CreatePart(header)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		h.t.Fatal(err)
	}
	if err := form.WriteField("caption", caption); err != nil {
		h.t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		h.t.Fatal(err)
	}
	return h.do(http.MethodPost, h.console.URL+"/api/contacts/"+userNumber+"/messages", body.Bytes(),
		map[string]string{"Content-Type": form.FormDataContentType()})
}

func TestMediaDownloadFlow(t *testing.T) {
	h := newHarness(t, nil)
	content := []byte("\x89PNG not really an image")

	// The consumer resolves and downloads while the webhook POST is still open.
	// This deadlocks if delivery ever holds anything a Graph request needs.
	downloaded := make(chan []byte, 1)
	h.consumer.configure(0, func(envelope webhook.Envelope) {
		value := envelope.Entry[0].Changes[0].Value
		if len(value.Messages) != 1 || value.Messages[0].Image == nil {
			return
		}
		lookup := h.do(http.MethodGet, h.graph.URL+"/v20.0/"+value.Messages[0].Image.ID, nil, authHeader)
		var resolved struct {
			MessagingProduct string `json:"messaging_product"`
			URL              string `json:"url"`
			MimeType         string `json:"mime_type"`
			SHA256           string `json:"sha256"`
			FileSize         string `json:"file_size"`
			ID               string `json:"id"`
		}
		lookup.decode(t, &resolved)
		if resolved.MimeType != "image/png" || resolved.FileSize != fmt.Sprint(len(content)) || resolved.MessagingProduct != "whatsapp" {
			t.Errorf("lookup = %s", lookup.body)
		}
		if !strings.HasPrefix(resolved.URL, h.graph.URL) {
			t.Errorf("url %q is not on wasim's own host", resolved.URL)
		}

		if result := h.do(http.MethodGet, resolved.URL, nil, nil); result.status != http.StatusUnauthorized {
			t.Errorf("download without a token: HTTP %d", result.status)
		}
		result := h.do(http.MethodGet, resolved.URL, nil, authHeader)
		if result.status != http.StatusOK || result.header.Get("Content-Type") != "image/png" {
			t.Errorf("download: HTTP %d, type %q", result.status, result.header.Get("Content-Type"))
		}
		downloaded <- result.body
	})

	created := h.uploadFile("ecg.png", "image/png", "Please look", content)
	if created.status != http.StatusCreated {
		t.Fatalf("upload: HTTP %d: %s", created.status, created.body)
	}
	select {
	case got := <-downloaded:
		if !bytes.Equal(got, content) {
			t.Errorf("downloaded %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("media was not downloaded during the webhook")
	}

	var message domain.Message
	created.decode(t, &message)
	image := h.waitForWebhook(messageIs(message.ID)).Messages[0].Image
	if image.Caption != "Please look" || image.MimeType != "image/png" {
		t.Errorf("image = %+v", image)
	}
	if _, err := base64.StdEncoding.DecodeString(image.SHA256); err != nil {
		t.Errorf("sha256 is not base64: %v", err)
	}
}

func TestDocumentMessageCarriesItsFilename(t *testing.T) {
	h := newHarness(t, nil)
	created := h.uploadFile("exam.pdf", "application/pdf", "", []byte("%PDF-1.7"))
	if created.status != http.StatusCreated {
		t.Fatalf("upload: HTTP %d: %s", created.status, created.body)
	}
	var message domain.Message
	created.decode(t, &message)
	got := h.waitForWebhook(messageIs(message.ID)).Messages[0]
	if got.Type != "document" || got.Document.Filename != "exam.pdf" {
		t.Errorf("message = %+v", got)
	}
}

func TestUploadRejectsWhatWhatsAppRefuses(t *testing.T) {
	h := newHarness(t, nil)
	tests := []struct {
		name, filename, mimeType string
		content                  []byte
	}{
		{"unsupported type", "a.gif", "image/gif", []byte("gif")},
		{"image over 5 MB", "big.png", "image/png", bytes.Repeat([]byte("x"), 5*1024*1024+1)},
		{"empty file", "a.png", "image/png", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if result := h.uploadFile(test.filename, test.mimeType, "", test.content); result.status != http.StatusBadRequest {
				t.Errorf("HTTP %d: %s", result.status, result.body)
			}
		})
	}
}

func TestExpiredOrTamperedMediaURLsAnswer404(t *testing.T) {
	h := newHarness(t, nil)
	created := h.uploadFile("a.png", "image/png", "", []byte("png"))
	var message domain.Message
	created.decode(t, &message)

	lookup := h.do(http.MethodGet, h.graph.URL+"/v20.0/"+message.MediaID, nil, authHeader)
	var resolved struct {
		URL string `json:"url"`
	}
	lookup.decode(t, &resolved)

	if result := h.do(http.MethodGet, resolved.URL+"0", nil, authHeader); result.status != http.StatusNotFound {
		t.Errorf("tampered: HTTP %d", result.status)
	}
	h.app.Service.SetClock(func() time.Time { return time.Now().Add(6 * time.Minute) })
	if result := h.do(http.MethodGet, resolved.URL, nil, authHeader); result.status != http.StatusNotFound {
		t.Errorf("expired: HTTP %d", result.status)
	}
	h.app.Service.SetClock(time.Now)
	fresh := h.do(http.MethodGet, h.graph.URL+"/v20.0/"+message.MediaID, nil, authHeader)
	fresh.decode(t, &resolved)
	if result := h.do(http.MethodGet, resolved.URL, nil, authHeader); result.status != http.StatusOK {
		t.Errorf("a new URL: HTTP %d", result.status)
	}
}

func TestTemplateListingFollowsPagingToTheEnd(t *testing.T) {
	h := newHarness(t, nil)
	next := h.graph.URL + "/v20.0/" + businessID + "/message_templates?fields=name,language,status,category,components&limit=2"
	names := []string{}
	for pages := 0; next != ""; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		result := h.do(http.MethodGet, next, nil, authHeader)
		if result.status != http.StatusOK {
			t.Fatalf("HTTP %d: %s", result.status, result.body)
		}
		var page struct {
			Data []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Language string `json:"language"`
				Status   string `json:"status"`
				Category string `json:"category"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		result.decode(t, &page)
		for _, template := range page.Data {
			if template.ID == "" || template.Language != "en_US" || template.Status != "APPROVED" || template.Category == "" {
				t.Errorf("template = %+v", template)
			}
			names = append(names, template.Name)
		}
		next = page.Paging.Next
		if next != "" && !strings.HasPrefix(next, h.graph.URL+"/v20.0/") {
			t.Errorf("next = %q", next)
		}
	}
	if got := strings.Join(names, ","); got != "hello_world,order_confirmation,appointment_reminder" {
		t.Errorf("names = %s", got)
	}
}

func TestTemplateListingOptions(t *testing.T) {
	h := newHarness(t, nil)
	base := h.graph.URL + "/v20.0/" + businessID + "/message_templates"

	t.Run("an unknown field fails the whole listing", func(t *testing.T) {
		h.expectGraphError(h.do(http.MethodGet, base+"?fields=name,nonsense", nil, authHeader), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
	})
	t.Run("only requested fields come back, plus the id", func(t *testing.T) {
		var page struct {
			Data []map[string]any `json:"data"`
		}
		h.do(http.MethodGet, base+"?fields=name", nil, authHeader).decode(t, &page)
		if len(page.Data[0]) != 2 || page.Data[0]["name"] == nil || page.Data[0]["id"] == nil {
			t.Errorf("entry = %v", page.Data[0])
		}
	})
	t.Run("filters by name", func(t *testing.T) {
		var page struct {
			Data []map[string]any `json:"data"`
		}
		h.do(http.MethodGet, base+"?name=hello_world", nil, authHeader).decode(t, &page)
		if len(page.Data) != 1 {
			t.Errorf("data = %v", page.Data)
		}
	})
	t.Run("a bad limit or cursor is refused", func(t *testing.T) {
		h.expectGraphError(h.do(http.MethodGet, base+"?limit=0", nil, authHeader), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
		h.expectGraphError(h.do(http.MethodGet, base+"?after=%21%21", nil, authHeader), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
	})
}

func TestMarkAsRead(t *testing.T) {
	h := newHarness(t, nil)
	first := h.userSays("first")
	second := h.userSays("second")

	result := h.send(`{"messaging_product":"whatsapp","status":"read","message_id":"` + second.ID + `"}`)
	if result.status != http.StatusOK || !strings.Contains(string(result.body), `"success":true`) {
		t.Fatalf("HTTP %d: %s", result.status, result.body)
	}
	var messages []domain.Message
	h.do(http.MethodGet, h.console.URL+"/api/contacts/"+userNumber+"/messages", nil, nil).decode(t, &messages)
	for _, message := range messages {
		if (message.ID == first.ID || message.ID == second.ID) && message.Status != domain.StatusRead {
			t.Errorf("message %s is %q", message.ID, message.Status)
		}
	}
	h.expectGraphError(h.send(`{"messaging_product":"whatsapp","status":"read","message_id":"wamid.none"}`), http.StatusBadRequest, cloudapi.CodeInvalidValue)
}

func TestWebhookRetryAndGiveUp(t *testing.T) {
	h := newHarness(t, nil)
	h.consumer.configure(http.StatusServiceUnavailable, nil)
	h.userSays("hi")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := h.app.Service.ListDeliveries(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if deliveries[0].State == domain.DeliveryFailed {
			if deliveries[0].Attempts != 3 || deliveries[0].LastStatus != http.StatusServiceUnavailable {
				t.Errorf("delivery = %+v", deliveries[0])
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the webhook never gave up")
}

func TestRedeliverSendsTheSameBytes(t *testing.T) {
	h := newHarness(t, nil)
	message := h.userSays("café")
	h.waitForWebhook(messageIs(message.ID))

	deliveries, err := h.app.Service.ListDeliveries(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/deliveries/%d/redeliver", deliveries[0].ID)
	if result := h.consolePost(path, map[string]any{}); result.status != http.StatusAccepted {
		t.Fatalf("HTTP %d: %s", result.status, result.body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.consumer.mu.Lock()
		count := len(h.consumer.bodies)
		var same bool
		if count == 2 {
			same = bytes.Equal(h.consumer.bodies[0], h.consumer.bodies[1])
		}
		h.consumer.mu.Unlock()
		if count == 2 {
			if !same {
				t.Error("the duplicate differs from the original")
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the duplicate never arrived")
}

func TestRedeliverWithBadSignatureIsRejectedByTheConsumer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.WebhookAttempts = 1 })
	message := h.userSays("hi")
	h.waitForWebhook(messageIs(message.ID))
	deliveries, err := h.app.Service.ListDeliveries(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	h.consolePost(fmt.Sprintf("/api/deliveries/%d/redeliver", deliveries[0].ID), map[string]any{"bad_signature": true})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		latest, err := h.app.Service.ListDeliveries(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if latest[0].State == domain.DeliveryFailed {
			if latest[0].LastStatus != http.StatusUnauthorized || !latest[0].BadSignature {
				t.Errorf("delivery = %+v", latest[0])
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the consumer did not reject the bad signature")
}

func TestConsoleRefusesCrossOriginWrites(t *testing.T) {
	h := newHarness(t, nil)
	target := h.console.URL + "/api/contacts"
	payload := []byte(`{"wa_id":"15550000666","name":"Evil"}`)

	evil := h.do(http.MethodPost, target, payload, map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"})
	if evil.status != http.StatusForbidden {
		t.Errorf("cross-origin: HTTP %d", evil.status)
	}
	same := h.do(http.MethodPost, target, payload, map[string]string{"Origin": h.console.URL, "Content-Type": "application/json"})
	if same.status != http.StatusCreated {
		t.Errorf("same origin: HTTP %d: %s", same.status, same.body)
	}
}

func TestConsoleServesTheUI(t *testing.T) {
	h := newHarness(t, nil)
	index := h.do(http.MethodGet, h.console.URL+"/", nil, nil)
	if index.status != http.StatusOK || !bytes.Contains(index.body, []byte("wasim console")) {
		t.Errorf("index: HTTP %d", index.status)
	}
	if script := h.do(http.MethodGet, h.console.URL+"/app.js", nil, nil); script.status != http.StatusOK {
		t.Errorf("app.js: HTTP %d", script.status)
	}
}

func TestReset(t *testing.T) {
	h := newHarness(t, nil)
	h.userSays("hi")
	created := h.uploadFile("a.png", "image/png", "", []byte("png"))
	var message domain.Message
	created.decode(t, &message)

	if result := h.consolePost("/api/reset", map[string]any{}); result.status != http.StatusNoContent {
		t.Fatalf("reset: HTTP %d: %s", result.status, result.body)
	}

	var contacts []domain.Contact
	h.do(http.MethodGet, h.console.URL+"/api/contacts", nil, nil).decode(t, &contacts)
	if len(contacts) != 2 {
		t.Errorf("contacts = %+v, want only the seeded ones", contacts)
	}
	var messages []domain.Message
	h.do(http.MethodGet, h.console.URL+"/api/contacts/"+userNumber+"/messages", nil, nil).decode(t, &messages)
	if len(messages) != 0 {
		t.Errorf("messages = %+v", messages)
	}
	lookup := h.do(http.MethodGet, h.graph.URL+"/v20.0/"+message.MediaID, nil, authHeader)
	h.expectGraphError(lookup, http.StatusBadRequest, cloudapi.CodeInvalidParameter)

	templates := h.do(http.MethodGet, h.graph.URL+"/v20.0/"+businessID+"/message_templates?name=hello_world", nil, authHeader)
	if !bytes.Contains(templates.body, []byte("hello_world")) {
		t.Errorf("templates were cleared: %s", templates.body)
	}
}

func TestVerifyWebhookFromTheConsole(t *testing.T) {
	h := newHarness(t, nil)
	result := h.consolePost("/api/webhook/verify", map[string]any{})
	if result.status != http.StatusOK {
		t.Fatalf("HTTP %d: %s", result.status, result.body)
	}
	var handshake webhook.HandshakeResult
	result.decode(t, &handshake)
	if handshake.Verified {
		t.Errorf("the fake consumer never echoes, got %+v", handshake)
	}

	unconfigured := newHarness(t, func(c *config.Config) { c.WebhookURL = "" })
	if got := unconfigured.consolePost("/api/webhook/verify", map[string]any{}); got.status != http.StatusBadRequest {
		t.Errorf("without a URL: HTTP %d", got.status)
	}
}

func TestWamidWithSlashesWorksInConsolePaths(t *testing.T) {
	h := newHarness(t, nil)
	h.userSays("hi")
	id := h.expectSent(h.sendText(userNumber, "hello"))
	if result := h.consolePost("/api/messages/"+url.PathEscape(id)+"/status", map[string]any{"status": "read"}); result.status != http.StatusAccepted {
		t.Errorf("HTTP %d: %s", result.status, result.body)
	}
	if result := h.consolePost("/api/messages/"+url.PathEscape("wamid.a/b+c=")+"/status", map[string]any{"status": "read"}); result.status != http.StatusNotFound {
		t.Errorf("unknown wamid with slashes: HTTP %d", result.status)
	}
}

func TestAppliedStatusReachesTheConsole(t *testing.T) {
	h := newHarness(t, nil)
	h.userSays("hi")
	id := h.expectSent(h.sendText(userNumber, "hello"))
	h.waitForWebhook(statusIs(id, "delivered"))
	var messages []domain.Message
	h.do(http.MethodGet, h.console.URL+"/api/contacts/"+userNumber+"/messages", nil, nil).decode(t, &messages)
	if got := messages[len(messages)-1]; got.ID != id || got.Status != domain.StatusDelivered {
		t.Errorf("message = %+v", got)
	}
}
