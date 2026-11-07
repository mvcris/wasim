package console_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/auth"
	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/console"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/events"
	"github.com/mvcris/wasim/internal/sim"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const user = "15550000001"

type fixture struct {
	t       *testing.T
	server  *httptest.Server
	service *sim.Service
	broker  *events.Broker
	users   *auth.Users
	clock   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	broker := events.NewBroker()
	service := sim.New(database, sim.Config{
		Account:            webhook.Account{BusinessAccountID: "2", PhoneNumberID: "1", DisplayPhoneNumber: "15550001234"},
		PublicURL:          "http://wasim.test",
		MediaDir:           t.TempDir(),
		WebhookURL:         "http://app.test/webhook",
		AutoStatuses:       []domain.MessageStatus{domain.StatusSent},
		EnforceWindow:      true,
		WindowErrorChannel: cloudapi.ChannelAsync,
		EscapeUnicode:      true,
		Seed:               sim.Seed{Contacts: []sim.SeedContact{{WaID: user, Name: "Ana"}}},
	}, sim.Hooks{OnChange: broker.Publish}, slog.New(slog.DiscardHandler))
	if err := service.ApplySeed(ctx); err != nil {
		t.Fatal(err)
	}
	users := auth.NewUsers(database)
	handler, err := console.New(service, broker, auth.NewGuard(users), "v1.2.3", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, service: service, broker: broker, users: users, clock: time.Now()}
	service.SetClock(func() time.Time { return f.clock })
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
	return f
}

type reply struct {
	status int
	body   []byte
}

func (r reply) decode(t *testing.T, target any) {
	t.Helper()
	if err := json.Unmarshal(r.body, target); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (f *fixture) do(method, path, contentType string, body io.Reader, headers map[string]string) reply {
	f.t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply{status: response.StatusCode, body: content}
}

func (f *fixture) postJSON(path, body string) reply {
	f.t.Helper()
	return f.do(http.MethodPost, path, "application/json", strings.NewReader(body), nil)
}

func (f *fixture) get(path string) reply {
	f.t.Helper()
	return f.do(http.MethodGet, path, "", nil, nil)
}

func wantStatus(t *testing.T, got reply, status int) {
	t.Helper()
	if got.status != status {
		t.Fatalf("HTTP %d, want %d: %s", got.status, status, got.body)
	}
}

func TestInfo(t *testing.T) {
	f := newFixture(t)
	var info map[string]any
	got := f.get("/api/info")
	wantStatus(t, got, http.StatusOK)
	got.decode(t, &info)
	if info["version"] != "v1.2.3" || info["phone_number_id"] != "1" || info["graph_url"] != "http://wasim.test" || info["webhook_url"] != "http://app.test/webhook" || info["enforce_window"] != true {
		t.Errorf("info = %s", got.body)
	}
}

func TestTheUIIsServed(t *testing.T) {
	f := newFixture(t)
	for path, marker := range map[string]string{"/": "<title>wasim console</title>", "/app.js": "connectEvents", "/style.css": ".bubble"} {
		got := f.get(path)
		if got.status != http.StatusOK || !bytes.Contains(got.body, []byte(marker)) {
			t.Errorf("%s: HTTP %d", path, got.status)
		}
	}
}

func TestErrorCatalog(t *testing.T) {
	f := newFixture(t)
	var catalog []cloudapi.ErrorDefinition
	f.get("/api/errors").decode(t, &catalog)
	if len(catalog) != len(cloudapi.ErrorCatalog()) || catalog[0].Code != cloudapi.CodeOutsideWindow {
		t.Errorf("catalog = %+v", catalog)
	}
}

func TestContacts(t *testing.T) {
	f := newFixture(t)
	f.clock = f.clock.Add(time.Second)

	created := f.postJSON("/api/contacts", `{"wa_id":"+1 555 000 0002","name":"Bruno"}`)
	wantStatus(t, created, http.StatusCreated)

	var contacts []struct {
		WaID       string `json:"wa_id"`
		Name       string `json:"name"`
		WindowOpen bool   `json:"window_open"`
	}
	f.get("/api/contacts").decode(t, &contacts)
	if len(contacts) != 2 || contacts[0].WaID != "15550000002" || contacts[0].WindowOpen {
		t.Errorf("contacts = %+v", contacts)
	}
}

func TestWindowBadgesFollowTheServiceClock(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"hi"}`), http.StatusCreated)
	f.clock = f.clock.Add(25 * time.Hour)

	var contacts []struct {
		WindowOpen bool `json:"window_open"`
	}
	f.get("/api/contacts").decode(t, &contacts)
	if contacts[0].WindowOpen {
		t.Error("the console reports an open window the service already considers closed")
	}
}

func TestHoursAgoBackDatesAgainstTheServiceClock(t *testing.T) {
	f := newFixture(t)
	f.clock = time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)

	var message domain.Message
	got := f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"old","hours_ago":30}`)
	wantStatus(t, got, http.StatusCreated)
	got.decode(t, &message)

	if want := f.clock.Add(-30 * time.Hour); !message.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", message.Timestamp, want)
	}
}

func TestMessagesAndStatuses(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"hi"}`), http.StatusCreated)
	request := cloudapi.SendRequest{MessagingProduct: "whatsapp", To: user, Type: "text", Text: &cloudapi.TextContent{Body: "hello"}}
	sent, err := f.service.Send(context.Background(), request, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	var messages []domain.Message
	f.get("/api/contacts/"+user+"/messages").decode(t, &messages)
	if len(messages) != 2 || messages[1].ID != sent.MessageID {
		t.Fatalf("messages = %+v", messages)
	}

	wantStatus(t, f.postJSON("/api/messages/"+sent.MessageID+"/status", `{"status":"read"}`), http.StatusAccepted)
	wantStatus(t, f.postJSON("/api/messages/"+sent.MessageID+"/status", `{"status":"played"}`), http.StatusBadRequest)
	wantStatus(t, f.postJSON("/api/messages/wamid.nope/status", `{"status":"read"}`), http.StatusNotFound)
	wantStatus(t, f.postJSON("/api/messages/"+messages[0].ID+"/status", `{"status":"read"}`), http.StatusBadRequest)
}

func TestInvalidRequests(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		got    reply
		status int
	}{
		{name: "malformed JSON", got: f.postJSON("/api/contacts", `{"wa_id":`), status: http.StatusBadRequest},
		{name: "an invalid phone", got: f.postJSON("/api/contacts", `{"wa_id":"12"}`), status: http.StatusBadRequest},
		{name: "an empty text", got: f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text"}`), status: http.StatusBadRequest},
		{name: "expiring an unknown contact", got: f.postJSON("/api/contacts/15559999999/window/expire", ``), status: http.StatusNotFound},
		{name: "a non-numeric rule id", got: f.do(http.MethodDelete, "/api/failure-rules/abc", "", nil, nil), status: http.StatusBadRequest},
		{name: "an unknown rule", got: f.do(http.MethodDelete, "/api/failure-rules/42", "", nil, nil), status: http.StatusNotFound},
		{name: "an unknown error code", got: f.postJSON("/api/failure-rules", `{"code":7}`), status: http.StatusBadRequest},
		{name: "an unknown delivery", got: f.postJSON("/api/deliveries/42/redeliver", `{}`), status: http.StatusNotFound},
		{name: "a non-numeric delivery id", got: f.postJSON("/api/deliveries/x/redeliver", `{}`), status: http.StatusBadRequest},
		{name: "unknown media", got: f.get("/api/media/123"), status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantStatus(t, test.got, test.status)
			var body map[string]string
			test.got.decode(t, &body)
			if body["error"] == "" {
				t.Errorf("no error message: %s", test.got.body)
			}
		})
	}
}

func TestExpireWindow(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"hi"}`), http.StatusCreated)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/window/expire", ``), http.StatusNoContent)

	var contacts []struct {
		WindowOpen bool `json:"window_open"`
	}
	f.get("/api/contacts").decode(t, &contacts)
	if contacts[0].WindowOpen {
		t.Error("the window is still open")
	}
}

func TestFailureRules(t *testing.T) {
	f := newFixture(t)
	created := f.postJSON("/api/failure-rules", `{"contact_wa_id":"15550000001","code":131049,"times":2}`)
	wantStatus(t, created, http.StatusCreated)
	var rule domain.FailureRule
	created.decode(t, &rule)

	var rules []domain.FailureRule
	f.get("/api/failure-rules").decode(t, &rules)
	if len(rules) != 1 || rules[0].Remaining != 2 || rules[0].Channel != cloudapi.ChannelAsync {
		t.Fatalf("rules = %+v", rules)
	}

	wantStatus(t, f.do(http.MethodDelete, "/api/failure-rules/"+jsonNumber(rule.ID), "", nil, nil), http.StatusNoContent)
	f.get("/api/failure-rules").decode(t, &rules)
	if len(rules) != 0 {
		t.Errorf("rules after delete = %+v", rules)
	}
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestDeliveriesExposeTheSignedBytes(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"café"}`), http.StatusCreated)

	var deliveries []struct {
		ID      int64  `json:"id"`
		State   string `json:"state"`
		Payload string `json:"payload"`
	}
	f.get("/api/deliveries").decode(t, &deliveries)
	if len(deliveries) != 1 || deliveries[0].State != "pending" || !strings.Contains(deliveries[0].Payload, `caf\u00e9`) {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	wantStatus(t, f.postJSON("/api/deliveries/"+jsonNumber(deliveries[0].ID)+"/redeliver", `{"bad_signature":true}`), http.StatusAccepted)
	f.get("/api/deliveries").decode(t, &deliveries)
	if len(deliveries) != 2 {
		t.Errorf("got %d deliveries after a redelivery", len(deliveries))
	}
}

func TestVerifyWebhookReportsTheHandshake(t *testing.T) {
	f := newFixture(t)
	var result webhook.HandshakeResult
	got := f.postJSON("/api/webhook/verify", ``)
	wantStatus(t, got, http.StatusOK)
	got.decode(t, &result)
	if result.Verified || result.Error == "" || result.Challenge == "" {
		t.Errorf("result = %+v", result)
	}
}

func TestReset(t *testing.T) {
	f := newFixture(t)
	wantStatus(t, f.postJSON("/api/contacts/"+user+"/messages", `{"type":"text","text":"hi"}`), http.StatusCreated)
	wantStatus(t, f.postJSON("/api/reset", ``), http.StatusNoContent)

	var messages []domain.Message
	f.get("/api/contacts/"+user+"/messages").decode(t, &messages)
	if len(messages) != 0 {
		t.Errorf("messages after reset = %+v", messages)
	}
}

func upload(t *testing.T, fields map[string]string, filename, mimeType string, content []byte) (string, io.Reader) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		header := make(map[string][]string)
		header["Content-Disposition"] = []string{`form-data; name="file"; filename="` + filename + `"`}
		header["Content-Type"] = []string{mimeType}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), &body
}

func TestUploads(t *testing.T) {
	f := newFixture(t)
	path := "/api/contacts/" + user + "/messages"

	t.Run("an image becomes an image message and its bytes are served", func(t *testing.T) {
		contentType, body := upload(t, map[string]string{"caption": "look"}, "a.png", "image/png", []byte("\x89PNG fake"))
		got := f.do(http.MethodPost, path, contentType, body, nil)
		wantStatus(t, got, http.StatusCreated)
		var message domain.Message
		got.decode(t, &message)
		if message.Type != "image" || message.Rendered.Body != "look" || message.MediaID == "" {
			t.Fatalf("message = %+v", message)
		}
		media := f.get("/api/media/" + message.MediaID)
		if media.status != http.StatusOK || string(media.body) != "\x89PNG fake" {
			t.Errorf("media HTTP %d: %q", media.status, media.body)
		}
	})
	t.Run("a form without a file is refused", func(t *testing.T) {
		contentType, body := upload(t, map[string]string{"caption": "nothing"}, "", "", nil)
		wantStatus(t, f.do(http.MethodPost, path, contentType, body, nil), http.StatusBadRequest)
	})
	t.Run("a type WhatsApp refuses is refused", func(t *testing.T) {
		contentType, body := upload(t, nil, "a.gif", "image/gif", []byte("GIF89a"))
		wantStatus(t, f.do(http.MethodPost, path, contentType, body, nil), http.StatusBadRequest)
	})
	t.Run("a broken multipart body is refused", func(t *testing.T) {
		wantStatus(t, f.do(http.MethodPost, path, "multipart/form-data; boundary=x", strings.NewReader("garbage"), nil), http.StatusBadRequest)
	})
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	f := newFixture(t)
	foreign := map[string]string{"Origin": "https://evil.example"}

	got := f.do(http.MethodPost, "/api/reset", "application/json", nil, foreign)
	wantStatus(t, got, http.StatusForbidden)

	sameOrigin := map[string]string{"Origin": f.server.URL}
	wantStatus(t, f.do(http.MethodPost, "/api/reset", "application/json", nil, sameOrigin), http.StatusNoContent)
	wantStatus(t, f.do(http.MethodGet, "/api/info", "", nil, foreign), http.StatusOK)
	wantStatus(t, f.do(http.MethodPost, "/api/reset", "", nil, map[string]string{"Origin": "::not a url"}), http.StatusForbidden)
}

func TestEventsStreamSignalsChanges(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.server.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request) //nolint:bodyclose // a goroutine reads the body, the defer below closes it
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q", response.Header.Get("Content-Type"))
	}

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	next := func() string {
		select {
		case line := <-lines:
			return line
		case <-time.After(2 * time.Second):
			t.Fatal("the stream went quiet")
			return ""
		}
	}

	if line := next(); line != ": connected" {
		t.Fatalf("first line = %q", line)
	}
	if line := next(); line != "" {
		t.Fatalf("frame not terminated: %q", line)
	}
	f.broker.Publish()
	if line := next(); line != "data: changed" {
		t.Errorf("after a change got %q", line)
	}
}
