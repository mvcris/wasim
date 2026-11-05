package graphapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/graphapi"
	"github.com/mvcris/wasim/internal/sim"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const (
	phoneNumberID = "100000000000001"
	businessID    = "200000000000001"
	user          = "15550000001"
)

type fixture struct {
	t       *testing.T
	server  *httptest.Server
	service *sim.Service
}

func newFixture(t *testing.T, tokens []string, templates int) *fixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	seed := sim.Seed{Contacts: []sim.SeedContact{{WaID: user, Name: "Ana"}}}
	for index := range templates {
		seed.Templates = append(seed.Templates, cloudapi.TemplateDefinition{
			ID: fmt.Sprint(index + 1), Name: fmt.Sprintf("template_%03d", index), Language: "en_US",
			Status: "APPROVED", Category: "UTILITY", ParameterFormat: cloudapi.FormatPositional,
			Components: []cloudapi.TemplateComponent{{Type: "BODY", Text: "Hi"}},
		})
	}
	server := httptest.NewUnstartedServer(nil)
	service := sim.New(database, sim.Config{
		Account:            webhook.Account{BusinessAccountID: businessID, PhoneNumberID: phoneNumberID, DisplayPhoneNumber: "15550001234"},
		PublicURL:          "http://" + server.Listener.Addr().String(),
		MediaDir:           t.TempDir(),
		WindowErrorChannel: cloudapi.ChannelAsync,
		Seed:               seed,
	}, sim.Hooks{}, slog.New(slog.DiscardHandler))
	if err := service.ApplySeed(ctx); err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = graphapi.New(service, tokens, slog.New(slog.DiscardHandler))
	server.Start()
	t.Cleanup(server.Close)
	return &fixture{t: t, server: server, service: service}
}

type reply struct {
	status int
	body   []byte
	header http.Header
}

func (f *fixture) request(method, path, token string, body io.Reader) reply {
	f.t.Helper()
	target := path
	if !strings.HasPrefix(path, "http") {
		target = f.server.URL + path
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply{status: response.StatusCode, body: content, header: response.Header}
}

func (f *fixture) post(path, body string) reply {
	f.t.Helper()
	return f.request(http.MethodPost, path, "token", strings.NewReader(body))
}

func (f *fixture) get(path string) reply {
	f.t.Helper()
	return f.request(http.MethodGet, path, "token", nil)
}

type graphError struct {
	Error struct {
		Message   string `json:"message"`
		Type      string `json:"type"`
		Code      int    `json:"code"`
		Subcode   int    `json:"error_subcode"`
		TraceID   string `json:"fbtrace_id"`
		ErrorData *struct {
			MessagingProduct string `json:"messaging_product"`
			Details          string `json:"details"`
		} `json:"error_data"`
	} `json:"error"`
}

func wantError(t *testing.T, got reply, status, code int) graphError {
	t.Helper()
	if got.status != status {
		t.Fatalf("HTTP %d, want %d: %s", got.status, status, got.body)
	}
	if contentType := got.header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	var decoded graphError
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatalf("decode %s: %v", got.body, err)
	}
	if decoded.Error.Code != code {
		t.Fatalf("code = %d, want %d: %s", decoded.Error.Code, code, got.body)
	}
	if decoded.Error.TraceID == "" {
		t.Error("fbtrace_id is missing")
	}
	return decoded
}

func openWindow(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := f.service.Receive(context.Background(), sim.InboundInput{WaID: user, Type: sim.InboundText, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
}

const textBody = `{"messaging_product":"whatsapp","to":"+1 555 000 0001","type":"text","text":{"body":"hello"}}`

// Authentication.

func TestAuthentication(t *testing.T) {
	f := newFixture(t, []string{"good-token"}, 0)
	path := "/v20.0/" + businessID + "/message_templates"

	t.Run("a missing token answers 401 with code 190", func(t *testing.T) {
		decoded := wantError(t, f.request(http.MethodGet, path, "", nil), http.StatusUnauthorized, cloudapi.CodeAccessToken)
		if decoded.Error.Type != "OAuthException" {
			t.Errorf("type = %s", decoded.Error.Type)
		}
	})
	t.Run("an unknown token is refused", func(t *testing.T) {
		wantError(t, f.request(http.MethodGet, path, "bad-token", nil), http.StatusUnauthorized, cloudapi.CodeAccessToken)
	})
	t.Run("a configured token is accepted", func(t *testing.T) {
		if got := f.request(http.MethodGet, path, "good-token", nil); got.status != http.StatusOK {
			t.Errorf("HTTP %d: %s", got.status, got.body)
		}
	})
	t.Run("the access_token query parameter is accepted", func(t *testing.T) {
		if got := f.request(http.MethodGet, path+"?access_token=good-token", "", nil); got.status != http.StatusOK {
			t.Errorf("HTTP %d: %s", got.status, got.body)
		}
	})
	t.Run("the scheme is case insensitive", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
		request.Header.Set("Authorization", "bearer good-token")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("HTTP %d", response.StatusCode)
		}
	})
}

func TestAnyTokenIsAcceptedWhenNoneAreConfigured(t *testing.T) {
	f := newFixture(t, nil, 0)
	if got := f.request(http.MethodGet, "/v20.0/"+businessID+"/message_templates", "whatever", nil); got.status != http.StatusOK {
		t.Errorf("HTTP %d: %s", got.status, got.body)
	}
}

// Routing.

func TestUnknownRoutesAnswerUnknownObject(t *testing.T) {
	f := newFixture(t, nil, 0)
	for name, got := range map[string]reply{
		"a malformed version":        f.post("/version20/"+phoneNumberID+"/messages", textBody),
		"another phone number id":    f.post("/v20.0/999/messages", textBody),
		"another business account":   f.get("/v20.0/999/message_templates"),
		"an unknown path":            f.get("/v20.0/" + phoneNumberID + "/whatsapp_business_profile"),
		"an unknown object":          f.get("/v20.0/123456"),
		"a method the route lacks":   f.request(http.MethodDelete, "/v20.0/123456", "token", nil),
		"a version with no numbers":  f.get("/vX.Y/123456"),
		"a version without a minor":  f.get("/v20/123456"),
		"the root of the Graph host": f.get("/"),
	} {
		t.Run(name, func(t *testing.T) {
			decoded := wantError(t, got, http.StatusBadRequest, cloudapi.CodeInvalidParameter)
			if decoded.Error.Subcode != 33 || decoded.Error.Type != "GraphMethodException" {
				t.Errorf("error = %+v", decoded.Error)
			}
		})
	}
}

func TestEveryVersionPrefixIsServed(t *testing.T) {
	f := newFixture(t, nil, 1)
	for _, version := range []string{"v17.0", "v20.0", "v27.0", "v99.9"} {
		if got := f.get("/" + version + "/" + businessID + "/message_templates"); got.status != http.StatusOK {
			t.Errorf("%s: HTTP %d", version, got.status)
		}
	}
}

// Sending.

func TestSendResponseShape(t *testing.T) {
	f := newFixture(t, nil, 0)
	openWindow(t, f)

	got := f.post("/v20.0/"+phoneNumberID+"/messages", textBody)

	if got.status != http.StatusOK {
		t.Fatalf("HTTP %d: %s", got.status, got.body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatal(err)
	}
	contacts := decoded["contacts"].([]any)[0].(map[string]any)
	message := decoded["messages"].([]any)[0].(map[string]any)
	if decoded["messaging_product"] != "whatsapp" || contacts["input"] != "+1 555 000 0001" || contacts["wa_id"] != user {
		t.Errorf("response = %s", got.body)
	}
	if !strings.HasPrefix(message["id"].(string), "wamid.") {
		t.Errorf("id = %v", message["id"])
	}
	if _, present := message["message_status"]; present {
		t.Error("message_status should only appear on template sends")
	}
}

func TestTemplateSendsReportAccepted(t *testing.T) {
	f := newFixture(t, nil, 1)
	got := f.post("/v20.0/"+phoneNumberID+"/messages",
		`{"messaging_product":"whatsapp","to":"15550000001","type":"template","template":{"name":"template_000","language":{"code":"en_US"},"components":[]}}`)
	if got.status != http.StatusOK || !bytes.Contains(got.body, []byte(`"message_status":"accepted"`)) {
		t.Errorf("HTTP %d: %s", got.status, got.body)
	}
}

func TestSendErrors(t *testing.T) {
	f := newFixture(t, nil, 0)
	path := "/v20.0/" + phoneNumberID + "/messages"
	tests := []struct {
		name   string
		body   string
		status int
		code   int
	}{
		{name: "invalid JSON", body: `{"messaging_product":`, status: http.StatusBadRequest, code: cloudapi.CodeInvalidParameter},
		{name: "a validation failure", body: `{"messaging_product":"whatsapp","to":"15550000001","type":"text","text":{}}`, status: http.StatusBadRequest, code: cloudapi.CodeMissingParameter},
		{name: "a template that does not exist", body: `{"messaging_product":"whatsapp","to":"15550000001","type":"template","template":{"name":"nope","language":{"code":"en_US"}}}`, status: http.StatusNotFound, code: cloudapi.CodeTemplateNotAvailable},
		{name: "mark as read without message_id", body: `{"messaging_product":"whatsapp","status":"read"}`, status: http.StatusBadRequest, code: cloudapi.CodeMissingParameter},
		{name: "mark as read of an unknown message", body: `{"messaging_product":"whatsapp","status":"read","message_id":"wamid.nope"}`, status: http.StatusBadRequest, code: cloudapi.CodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded := wantError(t, f.post(path, test.body), test.status, test.code)
			if decoded.Error.ErrorData == nil || decoded.Error.ErrorData.MessagingProduct != "whatsapp" || decoded.Error.ErrorData.Details == "" {
				t.Errorf("error_data = %+v", decoded.Error.ErrorData)
			}
			if !strings.HasPrefix(decoded.Error.Message, fmt.Sprintf("(#%d) ", test.code)) {
				t.Errorf("message = %q", decoded.Error.Message)
			}
		})
	}
}

func TestOversizedBodiesAreRefused(t *testing.T) {
	f := newFixture(t, nil, 0)
	body := `{"messaging_product":"whatsapp","to":"15550000001","type":"text","text":{"body":"` + strings.Repeat("a", 5*1024*1024) + `"}}`
	wantError(t, f.post("/v20.0/"+phoneNumberID+"/messages", body), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
}

func TestMarkAsRead(t *testing.T) {
	f := newFixture(t, nil, 0)
	message, err := f.service.Receive(context.Background(), sim.InboundInput{WaID: user, Type: sim.InboundText, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	got := f.post("/v20.0/"+phoneNumberID+"/messages", fmt.Sprintf(`{"messaging_product":"whatsapp","status":"read","message_id":%q}`, message.ID))

	if got.status != http.StatusOK || strings.TrimSpace(string(got.body)) != `{"success":true}` {
		t.Errorf("HTTP %d: %s", got.status, got.body)
	}
}

// Media.

func TestMediaLookupAndDownload(t *testing.T) {
	f := newFixture(t, nil, 0)
	content := []byte("%PDF-1.4 test")
	media, err := f.service.SaveMedia(context.Background(), bytes.NewReader(content), "a.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}

	lookup := f.get("/v20.0/" + media.ID)
	if lookup.status != http.StatusOK {
		t.Fatalf("HTTP %d: %s", lookup.status, lookup.body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(lookup.body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["file_size"] != fmt.Sprint(len(content)) || decoded["mime_type"] != "application/pdf" || decoded["id"] != media.ID || decoded["messaging_product"] != "whatsapp" {
		t.Errorf("lookup = %s", lookup.body)
	}

	download := f.get(decoded["url"].(string))
	if download.status != http.StatusOK || !bytes.Equal(download.body, content) {
		t.Fatalf("download HTTP %d: %q", download.status, download.body)
	}
	if download.header.Get("Content-Type") != "application/pdf" || download.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v", download.header)
	}

	t.Run("the download needs a token", func(t *testing.T) {
		wantError(t, f.request(http.MethodGet, decoded["url"].(string), "", nil), http.StatusUnauthorized, cloudapi.CodeAccessToken)
	})
	t.Run("a tampered URL answers a plain 404", func(t *testing.T) {
		if got := f.get(strings.Replace(decoded["url"].(string), "hash=", "hash=0", 1)); got.status != http.StatusNotFound {
			t.Errorf("HTTP %d", got.status)
		}
	})
	t.Run("an unknown media id answers unknown object", func(t *testing.T) {
		wantError(t, f.get("/v20.0/1234567890"), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
	})
}

func TestServeMediaFileAnswers404WhenTheFileIsGone(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	graphapi.ServeMediaFile(recorder, request, domain.Media{ID: "1", Path: "/does/not/exist"}, slog.New(slog.DiscardHandler))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("HTTP %d", recorder.Code)
	}
}

func TestServeMediaFileSupportsRanges(t *testing.T) {
	path := t.TempDir() + "/file"
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=2-4")

	graphapi.ServeMediaFile(recorder, request, domain.Media{ID: "1", Path: path, MimeType: "text/plain", CreatedAt: time.Now()}, slog.New(slog.DiscardHandler))

	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "234" {
		t.Errorf("HTTP %d: %q", recorder.Code, recorder.Body.String())
	}
}

// Template listing.

type templateList struct {
	Data   []map[string]any `json:"data"`
	Paging *struct {
		Cursors struct {
			Before string `json:"before"`
			After  string `json:"after"`
		} `json:"cursors"`
		Next     string `json:"next"`
		Previous string `json:"previous"`
	} `json:"paging"`
}

func (f *fixture) listTemplates(query string) templateList {
	f.t.Helper()
	got := f.get("/v20.0/" + businessID + "/message_templates" + query)
	if got.status != http.StatusOK {
		f.t.Fatalf("HTTP %d: %s", got.status, got.body)
	}
	var decoded templateList
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		f.t.Fatal(err)
	}
	return decoded
}

func TestTemplateListDefaults(t *testing.T) {
	f := newFixture(t, nil, 30)

	page := f.listTemplates("")

	if len(page.Data) != 25 {
		t.Errorf("default page has %d templates, want 25", len(page.Data))
	}
	for _, field := range []string{"id", "name", "language", "status", "category", "components", "parameter_format"} {
		if _, present := page.Data[0][field]; !present {
			t.Errorf("default listing lacks %q", field)
		}
	}
	if page.Paging == nil || page.Paging.Next == "" || page.Paging.Previous != "" {
		t.Errorf("paging = %+v", page.Paging)
	}
}

func TestTemplateListLimitIsCappedAt100(t *testing.T) {
	f := newFixture(t, nil, 120)
	if page := f.listTemplates("?limit=500"); len(page.Data) != 100 {
		t.Errorf("got %d templates, want the cap of 100", len(page.Data))
	}
}

func TestTemplateListPagesBackwards(t *testing.T) {
	f := newFixture(t, nil, 5)
	first := f.listTemplates("?limit=2")
	second := f.get(first.Paging.Next)
	var decoded templateList
	if err := json.Unmarshal(second.body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Paging.Previous == "" {
		t.Fatal("the second page has no previous link")
	}

	back := f.get(decoded.Paging.Previous)
	var previous templateList
	if err := json.Unmarshal(back.body, &previous); err != nil {
		t.Fatal(err)
	}
	if len(previous.Data) != 2 || previous.Data[0]["name"] != first.Data[0]["name"] {
		t.Errorf("previous page = %+v", previous.Data)
	}
}

func TestTemplateListPagingURLsAreAbsoluteAndKeepTheQuery(t *testing.T) {
	f := newFixture(t, nil, 3)
	page := f.listTemplates("?limit=1&fields=name")
	if !strings.HasPrefix(page.Paging.Next, f.server.URL+"/v20.0/"+businessID+"/message_templates?") ||
		!strings.Contains(page.Paging.Next, "fields=name") || !strings.Contains(page.Paging.Next, "limit=1") {
		t.Errorf("next = %s", page.Paging.Next)
	}
}

func TestAnEmptyCatalogHasNoPaging(t *testing.T) {
	f := newFixture(t, nil, 0)
	got := f.get("/v20.0/" + businessID + "/message_templates")
	if strings.TrimSpace(string(got.body)) != `{"data":[]}` {
		t.Errorf("body = %s", got.body)
	}
}

func TestTemplateListFilters(t *testing.T) {
	f := newFixture(t, nil, 3)
	if page := f.listTemplates("?name=template_001"); len(page.Data) != 1 || page.Data[0]["name"] != "template_001" {
		t.Errorf("name filter = %+v", page.Data)
	}
	if page := f.listTemplates("?status=approved"); len(page.Data) != 3 {
		t.Errorf("status filter returned %d templates", len(page.Data))
	}
	if page := f.listTemplates("?status=REJECTED"); len(page.Data) != 0 {
		t.Errorf("status filter returned %d rejected templates", len(page.Data))
	}
}

func TestTemplateListFieldSelection(t *testing.T) {
	f := newFixture(t, nil, 1)
	page := f.listTemplates("?fields=name,%20status")
	entry := page.Data[0]
	if len(entry) != 3 || entry["name"] != "template_000" || entry["status"] != "APPROVED" || entry["id"] != "1" {
		t.Errorf("entry = %+v", entry)
	}
}

func TestTemplateListRefusesBadParameters(t *testing.T) {
	f := newFixture(t, nil, 1)
	base := "/v20.0/" + businessID + "/message_templates"
	for name, query := range map[string]string{
		"an unknown field":    "?fields=name,quality_score",
		"a zero limit":        "?limit=0",
		"a non-numeric limit": "?limit=ten",
		"a garbled after":     "?after=bm90LWEtbnVtYmVy",
		"a garbled before":    "?before=!!!",
	} {
		t.Run(name, func(t *testing.T) {
			wantError(t, f.get(base+query), http.StatusBadRequest, cloudapi.CodeInvalidParameter)
		})
	}
}
