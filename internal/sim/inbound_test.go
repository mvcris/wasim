package sim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/webhook"
)

// Inbound messages.

func TestReceiveTextQueuesTheWebhookAndOpensTheWindow(t *testing.T) {
	f := newFixture(t, nil)

	message, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Name: "Ana L.", Type: InboundText, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	value := inboundOf(t, f.deliveries()[0])
	got := value.Messages[0]
	if got.ID != message.ID || got.From != user || got.Type != "text" || got.Text.Body != "hello" || got.Timestamp != strconv.FormatInt(start.Unix(), 10) {
		t.Errorf("message = %+v", got)
	}
	if value.Contacts[0].Profile.Name != "Ana L." || value.Contacts[0].WaID != user {
		t.Errorf("contact = %+v", value.Contacts[0])
	}
	contact, err := f.store.GetContact(context.Background(), user)
	if err != nil || !contact.WindowOpen(f.clock) {
		t.Errorf("window not opened: %+v, %v", contact, err)
	}
	if message.Status != domain.StatusReceived || message.Direction != domain.DirectionInbound {
		t.Errorf("stored message = %+v", message)
	}
}

func TestReceiveRefusesAnEmptyText(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText})
	wantInvalid(t, err)
}

func TestReceiveRefusesAnUnknownType(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: "sticker"})
	wantInvalid(t, err)
}

func TestABackDatedMessageCarriesItsTimestampAndLeavesTheWindowClosed(t *testing.T) {
	f := newFixture(t, nil)
	at := start.Add(-30 * time.Hour)

	if _, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText, Text: "old", Timestamp: &at}); err != nil {
		t.Fatal(err)
	}

	if got := inboundOf(t, f.deliveries()[0]).Messages[0].Timestamp; got != strconv.FormatInt(at.Unix(), 10) {
		t.Errorf("timestamp = %s", got)
	}
	contact, _ := f.store.GetContact(context.Background(), user)
	if contact.WindowOpen(f.clock) {
		t.Error("a 30 hour old message opened the window")
	}
	if !f.deliveries()[0].NextAttemptAt.Equal(start) {
		t.Error("a back-dated message must still be delivered now")
	}
}

func TestQuotingAUserMessageNamesTheUserAsSender(t *testing.T) {
	f := newFixture(t, nil)
	quoted := f.userSays("first")

	if _, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText, Text: "second", ContextID: quoted.ID}); err != nil {
		t.Fatal(err)
	}

	context := inboundOf(t, f.deliveries()[1]).Messages[0].Context
	if context == nil || context.ID != quoted.ID || context.From != user {
		t.Errorf("context = %+v", context)
	}
}

func TestQuotingABusinessMessageNamesTheBusinessAsSender(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	sent, err := f.send(textRequest(user, "hi"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText, Text: "this one", ContextID: sent.MessageID}); err != nil {
		t.Fatal(err)
	}

	deliveries := f.deliveries()
	context := inboundOf(t, deliveries[len(deliveries)-1]).Messages[0].Context
	if context == nil || context.From != business {
		t.Errorf("context = %+v", context)
	}
}

func TestQuotingAnotherConversationIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	quoted := f.userSays("mine")
	_, err := f.service.Receive(context.Background(), InboundInput{WaID: stranger, Type: InboundText, Text: "yours?", ContextID: quoted.ID})
	wantInvalid(t, err)
}

func TestQuotingAnUnknownMessageIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText, Text: "?", ContextID: "wamid.nope"})
	wantInvalid(t, err)
}

func TestReplies(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	f.userSays("hello")
	buttons := cloudapi.SendRequest{MessagingProduct: "whatsapp", To: user, Type: cloudapi.TypeInteractive, Interactive: &cloudapi.Interactive{
		Type: "button",
		Body: &cloudapi.InteractiveText{Text: "Pick one"},
		Action: cloudapi.InteractiveAction{Buttons: []cloudapi.ReplyButton{
			{Type: "reply", Reply: struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}{ID: "yes-id", Title: "Yes"}},
		}},
	}}
	sentButtons, err := f.send(buttons)
	if err != nil {
		t.Fatal(err)
	}
	list := cloudapi.SendRequest{MessagingProduct: "whatsapp", To: user, Type: cloudapi.TypeInteractive, Interactive: &cloudapi.Interactive{
		Type:   "list",
		Body:   &cloudapi.InteractiveText{Text: "Shipping?"},
		Action: cloudapi.InteractiveAction{Button: "Options", Sections: []cloudapi.ListSection{{Rows: []cloudapi.ListRow{{ID: "fast", Title: "Express", Description: "Tomorrow"}}}}},
	}}
	sentList, err := f.send(list)
	if err != nil {
		t.Fatal(err)
	}
	sentTemplate, err := f.send(templateRequest(user, "order_confirmation", "Ana", "SKU-42"))
	if err != nil {
		t.Fatal(err)
	}
	tap := func(contextID, replyID string) (domain.Message, error) {
		return f.service.Receive(ctx, InboundInput{WaID: user, Type: InboundReply, ContextID: contextID, ReplyID: replyID})
	}
	lastInbound := func() webhook.InboundMessage {
		deliveries := f.deliveries()
		return inboundOf(t, deliveries[len(deliveries)-1]).Messages[0]
	}

	t.Run("a reply button reports button_reply", func(t *testing.T) {
		if _, err := tap(sentButtons.MessageID, "yes-id"); err != nil {
			t.Fatal(err)
		}
		got := lastInbound()
		if got.Type != "interactive" || got.Interactive.ButtonReply.ID != "yes-id" || got.Interactive.ButtonReply.Title != "Yes" || got.Context.ID != sentButtons.MessageID {
			t.Errorf("message = %+v", got)
		}
	})
	t.Run("a list row reports list_reply with its description", func(t *testing.T) {
		if _, err := tap(sentList.MessageID, "fast"); err != nil {
			t.Fatal(err)
		}
		got := lastInbound()
		if got.Interactive.Type != "list_reply" || got.Interactive.ListReply.Description != "Tomorrow" {
			t.Errorf("message = %+v", got.Interactive)
		}
	})
	t.Run("a template quick reply reports a button with the label as payload", func(t *testing.T) {
		message, err := tap(sentTemplate.MessageID, "Track order")
		if err != nil {
			t.Fatal(err)
		}
		got := lastInbound()
		if got.Type != "button" || got.Button.Payload != "Track order" || got.Button.Text != "Track order" || message.Rendered.Body != "Track order" {
			t.Errorf("message = %+v", got)
		}
	})
	t.Run("an option the message does not have is refused", func(t *testing.T) {
		_, err := tap(sentButtons.MessageID, "no-such-id")
		wantInvalid(t, err)
	})
	t.Run("a reply must point at a business message", func(t *testing.T) {
		inbound := f.userSays("not a menu")
		_, err := tap(inbound.ID, "yes-id")
		wantInvalid(t, err)
	})
	t.Run("a reply needs a message to point at", func(t *testing.T) {
		_, err := tap("", "yes-id")
		wantInvalid(t, err)
	})
}

// Media.

func pngBytes() []byte {
	return []byte("\x89PNG\r\n\x1a\nfake image bytes for the emulator")
}

func TestSaveMediaStoresTheFileAndItsDigest(t *testing.T) {
	f := newFixture(t, nil)
	content := pngBytes()

	media, err := f.service.SaveMedia(context.Background(), bytes.NewReader(content), "../../photo.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}

	digest := sha256.Sum256(content)
	if media.SHA256 != base64.StdEncoding.EncodeToString(digest[:]) || media.FileSize != int64(len(content)) || media.Filename != "photo.png" {
		t.Errorf("media = %+v", media)
	}
	if filepath.Dir(media.Path) != f.service.Config().MediaDir {
		t.Errorf("file written outside the media directory: %s", media.Path)
	}
	stored, err := os.ReadFile(media.Path)
	if err != nil || !bytes.Equal(stored, content) {
		t.Errorf("stored file differs: %v", err)
	}
}

func TestSaveMediaEnforcesThePlatformLimits(t *testing.T) {
	f := newFixture(t, nil)
	tests := []struct {
		name     string
		content  io.Reader
		mimeType string
	}{
		{name: "unsupported type", content: strings.NewReader("GIF89a"), mimeType: "image/gif"},
		{name: "empty file", content: strings.NewReader(""), mimeType: "image/png"},
		{name: "image over 5 MB", content: io.LimitReader(zeroReader{}, 5*1024*1024+1), mimeType: "image/jpeg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.service.SaveMedia(context.Background(), test.content, "file", test.mimeType)
			wantInvalid(t, err)
		})
	}
	entries, err := os.ReadDir(f.service.Config().MediaDir)
	if err != nil || len(entries) != 0 {
		t.Errorf("rejected uploads left %d files behind: %v", len(entries), err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestImageMessagesCarryAResolvableMediaObject(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	media, err := f.service.SaveMedia(ctx, bytes.NewReader(pngBytes()), "photo.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Receive(ctx, InboundInput{WaID: user, Type: InboundMedia, MediaID: media.ID, Caption: "look"}); err != nil {
		t.Fatal(err)
	}

	got := inboundOf(t, f.deliveries()[0]).Messages[0]
	if got.Type != "image" || got.Image == nil || got.Document != nil {
		t.Fatalf("message = %+v", got)
	}
	if got.Image.ID != media.ID || got.Image.Caption != "look" || got.Image.SHA256 != media.SHA256 || got.Image.Filename != "" {
		t.Errorf("image = %+v", got.Image)
	}
	if !strings.HasPrefix(got.Image.URL, "http://wasim.test"+AttachmentsPath+"?") {
		t.Errorf("url = %s", got.Image.URL)
	}
}

func TestDocumentMessagesCarryTheFilename(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	media, err := f.service.SaveMedia(ctx, strings.NewReader("%PDF-1.4"), "invoice.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Receive(ctx, InboundInput{WaID: user, Type: InboundMedia, MediaID: media.ID}); err != nil {
		t.Fatal(err)
	}

	got := inboundOf(t, f.deliveries()[0]).Messages[0]
	if got.Type != "document" || got.Document == nil || got.Document.Filename != "invoice.pdf" {
		t.Errorf("message = %+v", got)
	}
}

func TestReceiveRefusesUnknownMedia(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundMedia, MediaID: "123"})
	wantInvalid(t, err)
}

func TestResolveMedia(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	media, err := f.service.SaveMedia(ctx, bytes.NewReader(pngBytes()), "photo.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := f.service.ResolveMedia(ctx, media.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != media.ID || resolved.URL == "" {
		t.Errorf("resolved = %+v", resolved)
	}

	_, err = f.service.ResolveMedia(ctx, "999")
	if apiErr := apiErrorOf(t, err, cloudapi.CodeInvalidParameter); apiErr.Subcode != 33 {
		t.Errorf("subcode = %d, want 33", apiErr.Subcode)
	}
}

func TestOpenAttachment(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	media, err := f.service.SaveMedia(ctx, bytes.NewReader(pngBytes()), "photo.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.service.ResolveMedia(ctx, media.ID)
	if err != nil {
		t.Fatal(err)
	}
	minted, err := url.Parse(resolved.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := minted.Query()
	with := func(key, value string) url.Values {
		changed := url.Values{}
		for name, values := range query {
			changed[name] = append([]string(nil), values...)
		}
		changed.Set(key, value)
		return changed
	}

	t.Run("a fresh URL opens", func(t *testing.T) {
		opened, err := f.service.OpenAttachment(ctx, query)
		if err != nil || opened.ID != media.ID {
			t.Errorf("opened = %+v, %v", opened, err)
		}
	})
	for name, tampered := range map[string]url.Values{
		"a changed media id":  with("mid", "123"),
		"a stretched expiry":  with("ext", "9999999999"),
		"a malformed expiry":  with("ext", "soon"),
		"a forged signature":  with("hash", strings.Repeat("0", 64)),
		"no signature at all": with("hash", ""),
	} {
		t.Run(name+" is refused", func(t *testing.T) {
			if _, err := f.service.OpenAttachment(ctx, tampered); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("error = %v, want ErrNotFound", err)
			}
		})
	}
	t.Run("the URL dies after 5 minutes", func(t *testing.T) {
		f.advance(5*time.Minute + time.Second)
		if _, err := f.service.OpenAttachment(ctx, query); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestNewMediaIDsLookLikeGraphIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id := newMediaID()
		if len(id) != 16 || strings.Trim(id, "0123456789") != "" || seen[id] {
			t.Fatalf("bad or repeated media id %q", id)
		}
		seen[id] = true
	}
}

// Seeds.

func TestTheBuiltInSeedLoads(t *testing.T) {
	seed, err := LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Templates) != 3 || len(seed.Contacts) != 2 {
		t.Fatalf("seed = %d templates, %d contacts", len(seed.Templates), len(seed.Contacts))
	}
	for _, template := range seed.Templates {
		if !template.IsApproved() || template.ID == "" || template.ParameterFormat == "" {
			t.Errorf("template not normalized: %+v", template)
		}
	}
}

func TestTheExampleSeedLoads(t *testing.T) {
	if _, err := LoadSeed(filepath.Join("..", "..", "examples", "seed.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSeedFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.json")
	content := `{"templates":[{"name":"greet","language":"en_US","status":"rejected","category":"marketing","components":[{"type":"BODY","text":"Hi {{name}}"}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	seed, err := LoadSeed(path)
	if err != nil {
		t.Fatal(err)
	}

	template := seed.Templates[0]
	if template.Status != "REJECTED" || template.Category != "MARKETING" || template.ParameterFormat != cloudapi.FormatNamed {
		t.Errorf("template = %+v", template)
	}
	if template.ID != stableTemplateID("greet", "en_US") {
		t.Error("the id is not the stable one")
	}
}

func TestLoadSeedDefaultsStatusAndCategory(t *testing.T) {
	template := cloudapi.TemplateDefinition{Name: "x", Language: "en_US"}
	if err := normalizeTemplate(&template); err != nil {
		t.Fatal(err)
	}
	if template.Status != cloudapi.TemplateStatusApproved || template.Category != "UTILITY" {
		t.Errorf("template = %+v", template)
	}
}

func TestLoadSeedErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, path := range map[string]string{
		"a missing file":            filepath.Join(dir, "missing.json"),
		"invalid JSON":              write("broken.json", "{"),
		"a template without a name": write("nameless.json", `{"templates":[{"language":"en_US"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadSeed(path); err == nil {
				t.Error("LoadSeed accepted it")
			}
		})
	}
}

func TestStableTemplateIDs(t *testing.T) {
	first := stableTemplateID("hello_world", "en_US")
	if first != stableTemplateID("hello_world", "en_US") {
		t.Error("the id changed between calls")
	}
	if first == stableTemplateID("hello_world", "pt_BR") {
		t.Error("two languages share an id")
	}
	if len(first) != 16 {
		t.Errorf("id %q does not look like a Graph id", first)
	}
}
