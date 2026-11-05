package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const (
	user     = "15550000001"
	stranger = "15550000099"
	business = "15550001234"
)

var start = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t       *testing.T
	service *Service
	store   *store.Store
	clock   time.Time
	changes int
	queued  int
}

func newFixture(t *testing.T, adjust func(*Config)) *fixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	seed, err := LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	config := Config{
		Account: webhook.Account{
			BusinessAccountID:  "200000000000001",
			PhoneNumberID:      "100000000000001",
			DisplayPhoneNumber: business,
		},
		PublicURL:          "http://wasim.test",
		MediaDir:           t.TempDir(),
		AutoStatuses:       []domain.MessageStatus{domain.StatusSent, domain.StatusDelivered},
		StatusDelay:        time.Second,
		EnforceWindow:      true,
		WindowErrorChannel: cloudapi.ChannelAsync,
		Seed:               seed,
	}
	if adjust != nil {
		adjust(&config)
	}
	f := &fixture{t: t, store: database, clock: start}
	f.service = New(database, config, Hooks{
		OnChange:  func() { f.changes++ },
		OnEnqueue: func() { f.queued++ },
	}, slog.New(slog.DiscardHandler))
	f.service.SetClock(func() time.Time { return f.clock })
	if err := f.service.ApplySeed(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) advance(by time.Duration) {
	f.clock = f.clock.Add(by)
}

func textRequest(to, body string) cloudapi.SendRequest {
	return cloudapi.SendRequest{MessagingProduct: "whatsapp", To: to, Type: cloudapi.TypeText, Text: &cloudapi.TextContent{Body: body}}
}

func templateRequest(to, name string, parameters ...string) cloudapi.SendRequest {
	content := &cloudapi.TemplateContent{Name: name, Language: cloudapi.TemplateLanguage{Code: "en_US"}}
	if len(parameters) > 0 {
		component := cloudapi.ContentComponent{Type: "body"}
		for _, value := range parameters {
			component.Parameters = append(component.Parameters, cloudapi.ContentParameter{Type: "text", Text: value})
		}
		content.Components = []cloudapi.ContentComponent{component}
	}
	return cloudapi.SendRequest{MessagingProduct: "whatsapp", To: to, Type: cloudapi.TypeTemplate, Template: content}
}

func (f *fixture) send(request cloudapi.SendRequest) (SendResult, error) {
	f.t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.service.Send(context.Background(), request, raw)
}

func (f *fixture) userSays(text string) domain.Message {
	f.t.Helper()
	message, err := f.service.Receive(context.Background(), InboundInput{WaID: user, Type: InboundText, Text: text})
	if err != nil {
		f.t.Fatal(err)
	}
	return message
}

func (f *fixture) deliveries() []domain.Delivery {
	f.t.Helper()
	deliveries, err := f.store.ListDeliveries(context.Background(), 100)
	if err != nil {
		f.t.Fatal(err)
	}
	// Oldest first reads more naturally in assertions.
	for left, right := 0, len(deliveries)-1; left < right; left, right = left+1, right-1 {
		deliveries[left], deliveries[right] = deliveries[right], deliveries[left]
	}
	return deliveries
}

func statusOf(t *testing.T, delivery domain.Delivery) webhook.Status {
	t.Helper()
	var envelope webhook.Envelope
	if err := json.Unmarshal(delivery.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	statuses := envelope.Entry[0].Changes[0].Value.Statuses
	if len(statuses) != 1 {
		t.Fatalf("delivery %d is not a status webhook: %s", delivery.ID, delivery.Body)
	}
	return statuses[0]
}

func inboundOf(t *testing.T, delivery domain.Delivery) webhook.Value {
	t.Helper()
	var envelope webhook.Envelope
	if err := json.Unmarshal(delivery.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Entry[0].Changes[0].Value
}

func wantAPIError(t *testing.T, err error, code int) *cloudapi.APIError {
	t.Helper()
	var apiErr *cloudapi.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want Graph error %d", err, code)
	}
	if apiErr.Definition.Code != code {
		t.Fatalf("code = %d, want %d (%s)", apiErr.Definition.Code, code, apiErr.Definition.Details)
	}
	return apiErr
}

func wantInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// Sending.

func TestSendInsideTheWindowSchedulesAutomaticStatuses(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")

	result, err := f.send(textRequest("+1 (555) 000-0001", "hi"))
	if err != nil {
		t.Fatal(err)
	}

	if result.Input != "+1 (555) 000-0001" || result.WaID != user || result.Template {
		t.Errorf("result = %+v", result)
	}
	deliveries := f.deliveries()[1:]
	if len(deliveries) != 2 {
		t.Fatalf("got %d status webhooks, want sent and delivered", len(deliveries))
	}
	for index, want := range []string{"sent", "delivered"} {
		status := statusOf(t, deliveries[index])
		if status.Status != want || status.ID != result.MessageID || status.RecipientID != user {
			t.Errorf("status %d = %+v", index, status)
		}
		if wantAt := start.Add(time.Duration(index+1) * time.Second); !deliveries[index].NextAttemptAt.Equal(wantAt) {
			t.Errorf("status %s due at %v, want %v", want, deliveries[index].NextAttemptAt, wantAt)
		}
	}
}

func TestServiceMessagesArePricedAsFree(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	if _, err := f.send(textRequest(user, "hi")); err != nil {
		t.Fatal(err)
	}

	pricing := statusOf(t, f.deliveries()[1]).Pricing
	if pricing == nil || pricing.Billable || pricing.Type != "free_customer_service" || pricing.Category != "service" || pricing.PricingModel != "PMP" {
		t.Errorf("pricing = %+v", pricing)
	}
}

func TestTemplatesArePricedByTheirCategory(t *testing.T) {
	f := newFixture(t, nil)
	request := templateRequest(user, "appointment_reminder")
	request.Template.Components = []cloudapi.ContentComponent{{Type: "body", Parameters: []cloudapi.ContentParameter{
		{Type: "text", ParameterName: "first_name", Text: "Ana"},
		{Type: "text", ParameterName: "date", Text: "Friday"},
	}}}

	result, err := f.send(request)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Template {
		t.Error("result does not report a template send")
	}
	pricing := statusOf(t, f.deliveries()[0]).Pricing
	if pricing == nil || !pricing.Billable || pricing.Type != "regular" || pricing.Category != "marketing" {
		t.Errorf("pricing = %+v", pricing)
	}
}

func TestReadStatusesCarryNoPricing(t *testing.T) {
	f := newFixture(t, func(config *Config) {
		config.AutoStatuses = []domain.MessageStatus{domain.StatusRead}
	})
	f.userSays("hello")
	if _, err := f.send(textRequest(user, "hi")); err != nil {
		t.Fatal(err)
	}

	if pricing := statusOf(t, f.deliveries()[1]).Pricing; pricing != nil {
		t.Errorf("read status has pricing %+v", pricing)
	}
}

func TestSendValidatesTheRequest(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.send(cloudapi.SendRequest{MessagingProduct: "sms", To: user, Text: &cloudapi.TextContent{Body: "hi"}})
	wantAPIError(t, err, cloudapi.CodeInvalidParameter)
}

func TestSendRejectsAnInvalidRecipient(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.send(textRequest("not-a-number", "hi"))
	wantAPIError(t, err, cloudapi.CodeInvalidValue)
}

func TestSendToTheBusinessNumberIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.send(textRequest(business, "hi"))
	wantAPIError(t, err, cloudapi.CodeSenderIsRecipient)
}

func TestFreeFormOutsideTheWindowFailsByWebhook(t *testing.T) {
	f := newFixture(t, nil)

	result, err := f.send(textRequest(stranger, "hi"))
	if err != nil {
		t.Fatalf("an async failure must still accept the send: %v", err)
	}

	deliveries := f.deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("got %d webhooks, want one failed status", len(deliveries))
	}
	status := statusOf(t, deliveries[0])
	if status.ID != result.MessageID || status.Status != "failed" || status.Errors[0].Code != cloudapi.CodeOutsideWindow {
		t.Errorf("status = %+v", status)
	}
}

func TestFreeFormOutsideTheWindowCanFailSynchronously(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.WindowErrorChannel = cloudapi.ChannelSync })
	_, err := f.send(textRequest(stranger, "hi"))
	wantAPIError(t, err, cloudapi.CodeOutsideWindow)
}

func TestTheWindowClosesAfter24Hours(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.WindowErrorChannel = cloudapi.ChannelSync })
	f.userSays("hello")
	f.advance(domain.ServiceWindow)

	_, err := f.send(textRequest(user, "hi"))
	wantAPIError(t, err, cloudapi.CodeOutsideWindow)
}

func TestWindowEnforcementCanBeTurnedOff(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.EnforceWindow = false })
	if _, err := f.send(textRequest(stranger, "hi")); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, f.deliveries()[0]); status.Status != "sent" {
		t.Errorf("first status = %s, want sent", status.Status)
	}
}

func TestTemplatesGoOutWithTheWindowClosed(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.send(templateRequest(stranger, "order_confirmation", "Ana", "SKU-42")); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, f.deliveries()[0]); status.Status != "sent" {
		t.Errorf("first status = %s, want sent", status.Status)
	}
}

func TestUnknownTemplatesAreNotAvailable(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.send(templateRequest(user, "does_not_exist"))
	wantAPIError(t, err, cloudapi.CodeTemplateNotAvailable)
}

func TestTemplatesInAnotherLanguageAreNotAvailable(t *testing.T) {
	f := newFixture(t, nil)
	request := templateRequest(user, "hello_world")
	request.Template.Language.Code = "pt_BR"
	_, err := f.send(request)
	wantAPIError(t, err, cloudapi.CodeTemplateNotAvailable)
}

func TestUnapprovedTemplatesAreNotAvailable(t *testing.T) {
	f := newFixture(t, func(config *Config) {
		config.Seed.Templates = append(config.Seed.Templates, cloudapi.TemplateDefinition{
			ID: "1", Name: "pending_review", Language: "en_US", Status: "PENDING", Category: "UTILITY",
			Components: []cloudapi.TemplateComponent{{Type: "BODY", Text: "Hi"}},
		})
	})
	_, err := f.send(templateRequest(user, "pending_review"))
	wantAPIError(t, err, cloudapi.CodeTemplateNotAvailable)
}

func TestTemplateParametersAreChecked(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.send(templateRequest(user, "order_confirmation", "only one"))
	wantAPIError(t, err, cloudapi.CodeTemplateParamCount)
}

func TestTheSendKeepsTheRawRequestAndContext(t *testing.T) {
	f := newFixture(t, nil)
	quoted := f.userSays("hello")
	request := textRequest(user, "hi")
	request.Context = &cloudapi.SendContext{MessageID: quoted.ID}

	result, err := f.send(request)
	if err != nil {
		t.Fatal(err)
	}

	message, err := f.store.GetMessage(context.Background(), result.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.ContextID != quoted.ID || !bytes.Contains(message.Raw, []byte(`"body":"hi"`)) || message.Status != domain.StatusAccepted {
		t.Errorf("message = %+v", message)
	}
}

// Failure rules.

func TestSyncFailureRulesAreConsumedEvenThoughTheSendFails(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	if _, err := f.service.AddFailureRule(context.Background(), FailureRuleInput{Code: cloudapi.CodeThroughput}); err != nil {
		t.Fatal(err)
	}

	_, err := f.send(textRequest(user, "hi"))
	wantAPIError(t, err, cloudapi.CodeThroughput)

	if _, err := f.send(textRequest(user, "hi again")); err != nil {
		t.Errorf("the one-shot rule was not consumed: %v", err)
	}
}

func TestAsyncFailureRulesAcceptTheSendThenFail(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	if _, err := f.service.AddFailureRule(context.Background(), FailureRuleInput{ContactWaID: user, Code: cloudapi.CodeEcosystemEngagement}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.send(textRequest(user, "hi")); err != nil {
		t.Fatal(err)
	}

	deliveries := f.deliveries()[1:]
	status := statusOf(t, deliveries[0])
	if len(deliveries) != 1 || status.Status != "failed" || status.Errors[0].Code != cloudapi.CodeEcosystemEngagement {
		t.Errorf("deliveries = %d, first status = %+v", len(deliveries), status)
	}
}

func TestFailureRulesBeatTheWindowCheck(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.service.AddFailureRule(context.Background(), FailureRuleInput{Code: cloudapi.CodePairRateLimit}); err != nil {
		t.Fatal(err)
	}
	_, err := f.send(textRequest(stranger, "hi"))
	wantAPIError(t, err, cloudapi.CodePairRateLimit)
}

func TestAddFailureRule(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	t.Run("takes the code's default channel", func(t *testing.T) {
		rule, err := f.service.AddFailureRule(ctx, FailureRuleInput{Code: cloudapi.CodeOutsideWindow})
		if err != nil {
			t.Fatal(err)
		}
		if rule.Channel != cloudapi.ChannelAsync || rule.Remaining != 1 {
			t.Errorf("rule = %+v", rule)
		}
	})
	t.Run("keeps an explicit channel and count", func(t *testing.T) {
		rule, err := f.service.AddFailureRule(ctx, FailureRuleInput{Code: cloudapi.CodeOutsideWindow, Channel: cloudapi.ChannelSync, Times: domain.RuleSticky})
		if err != nil {
			t.Fatal(err)
		}
		if rule.Channel != cloudapi.ChannelSync || rule.Remaining != domain.RuleSticky {
			t.Errorf("rule = %+v", rule)
		}
	})
	for name, input := range map[string]FailureRuleInput{
		"refuses an unknown code":     {Code: 999},
		"refuses an unknown channel":  {Code: cloudapi.CodeOutsideWindow, Channel: "carrier pigeon"},
		"refuses a count below -1":    {Code: cloudapi.CodeOutsideWindow, Times: -2},
		"refuses a code with no rule": {Code: 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.service.AddFailureRule(ctx, input)
			wantInvalid(t, err)
		})
	}
}

func TestDeleteFailureRule(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	rule, err := f.service.AddFailureRule(ctx, FailureRuleInput{Code: cloudapi.CodeThroughput, Times: domain.RuleSticky})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.service.DeleteFailureRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}

	rules, err := f.service.ListFailureRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Errorf("rules = %+v, %v", rules, err)
	}
	if err := f.service.DeleteFailureRule(ctx, rule.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

// Statuses, duplicates and the handshake.

func TestSetStatusSendsAWebhookOnDemand(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.AutoStatuses = nil })
	f.userSays("hello")
	result, err := f.send(textRequest(user, "hi"))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.service.SetStatus(context.Background(), result.MessageID, domain.StatusRead, 0); err != nil {
		t.Fatal(err)
	}

	deliveries := f.deliveries()
	if status := statusOf(t, deliveries[len(deliveries)-1]); status.Status != "read" {
		t.Errorf("status = %+v", status)
	}
}

func TestSetStatusFailedDefaultsToUndeliverable(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.AutoStatuses = nil })
	f.userSays("hello")
	result, err := f.send(textRequest(user, "hi"))
	if err != nil {
		t.Fatal(err)
	}

	if err := f.service.SetStatus(context.Background(), result.MessageID, domain.StatusFailed, 0); err != nil {
		t.Fatal(err)
	}

	deliveries := f.deliveries()
	if status := statusOf(t, deliveries[len(deliveries)-1]); status.Errors[0].Code != cloudapi.CodeUndeliverable {
		t.Errorf("status = %+v", status)
	}
}

func TestSetStatusRefusesUserMessages(t *testing.T) {
	f := newFixture(t, nil)
	inbound := f.userSays("hello")
	wantInvalid(t, f.service.SetStatus(context.Background(), inbound.ID, domain.StatusRead, 0))
}

func TestSetStatusRefusesUnknownMessages(t *testing.T) {
	f := newFixture(t, nil)
	err := f.service.SetStatus(context.Background(), "wamid.nope", domain.StatusRead, 0)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestRedeliverCopiesTheExactBytes(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	original := f.deliveries()[0]

	if err := f.service.Redeliver(context.Background(), original.ID, true); err != nil {
		t.Fatal(err)
	}

	copied := f.deliveries()[1]
	if !bytes.Equal(copied.Body, original.Body) || !copied.BadSignature || copied.MessageStatus != "" {
		t.Errorf("copy = %+v", copied)
	}
	if f.queued != 2 {
		t.Errorf("the dispatcher was woken %d times, want 2", f.queued)
	}
}

func TestRedeliverRefusesUnknownWebhooks(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.service.Redeliver(context.Background(), 42, false); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestVerifyWebhookNeedsAURL(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.VerifyWebhook(context.Background())
	wantInvalid(t, err)
}

func TestMarkReadMarksEarlierUserMessages(t *testing.T) {
	f := newFixture(t, nil)
	first := f.userSays("one")
	second := f.userSays("two")

	if err := f.service.MarkRead(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{first.ID, second.ID} {
		message, err := f.store.GetMessage(context.Background(), id)
		if err != nil || message.Status != domain.StatusRead {
			t.Errorf("message %s status = %s, %v", id, message.Status, err)
		}
	}
}

func TestMarkReadRefusesBusinessMessages(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")
	result, err := f.send(textRequest(user, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	wantAPIError(t, f.service.MarkRead(context.Background(), result.MessageID), cloudapi.CodeInvalidValue)
}

func TestMarkReadRefusesUnknownMessages(t *testing.T) {
	f := newFixture(t, nil)
	wantAPIError(t, f.service.MarkRead(context.Background(), "wamid.nope"), cloudapi.CodeInvalidValue)
}

// Contacts and windows.

func TestCreateContact(t *testing.T) {
	f := newFixture(t, nil)
	contact, err := f.service.CreateContact(context.Background(), "+1 555 000 0042", "Dana")
	if err != nil {
		t.Fatal(err)
	}
	if contact.WaID != "15550000042" || contact.Name != "Dana" || contact.LastInboundAt != nil {
		t.Errorf("contact = %+v", contact)
	}
}

func TestCreateContactRefusesAnInvalidNumber(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.service.CreateContact(context.Background(), "12", "Too short")
	wantInvalid(t, err)
}

func TestAContactFirstSeenAsARecipientGetsAName(t *testing.T) {
	f := newFixture(t, func(config *Config) { config.EnforceWindow = false })
	if _, err := f.send(textRequest("15550009876", "hi")); err != nil {
		t.Fatal(err)
	}
	contact, err := f.store.GetContact(context.Background(), "15550009876")
	if err != nil || contact.Name != "User 9876" {
		t.Errorf("contact = %+v, %v", contact, err)
	}
}

func TestExpireWindow(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")

	if err := f.service.ExpireWindow(context.Background(), user); err != nil {
		t.Fatal(err)
	}

	contact, err := f.store.GetContact(context.Background(), user)
	if err != nil || contact.WindowOpen(f.clock) {
		t.Errorf("window still open: %+v, %v", contact, err)
	}
	if err := f.service.ExpireWindow(context.Background(), "15559999999"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown contact = %v, want ErrNotFound", err)
	}
}

func TestNowFollowsTheClock(t *testing.T) {
	f := newFixture(t, nil)
	f.advance(time.Hour)
	if got := f.service.Now(); !got.Equal(start.Add(time.Hour)) {
		t.Errorf("Now = %v", got)
	}
}

func TestReadModelsPassThrough(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	f.userSays("hello")

	contacts, err := f.service.ListContacts(ctx)
	if err != nil || len(contacts) != 2 || contacts[0].WaID != user {
		t.Errorf("contacts = %+v, %v", contacts, err)
	}
	messages, err := f.service.ListMessages(ctx, user)
	if err != nil || len(messages) != 1 {
		t.Errorf("messages = %+v, %v", messages, err)
	}
	page, err := f.service.ListTemplates(ctx, store.TemplateFilter{Limit: 10})
	if err != nil || len(page.Templates) != 3 {
		t.Errorf("templates = %+v, %v", page, err)
	}
	deliveries, err := f.service.ListDeliveries(ctx, 10)
	if err != nil || len(deliveries) != 1 {
		t.Errorf("deliveries = %+v, %v", deliveries, err)
	}
	if f.service.Config().Account.DisplayPhoneNumber != business {
		t.Error("Config does not return the settings")
	}
}

// Seeds and reset.

func TestApplySeedIsIdempotent(t *testing.T) {
	f := newFixture(t, nil)
	f.userSays("hello")

	if err := f.service.ApplySeed(context.Background()); err != nil {
		t.Fatal(err)
	}

	messages, err := f.store.ListMessages(context.Background(), user)
	if err != nil || len(messages) != 1 {
		t.Errorf("seeding again touched the conversation: %d messages, %v", len(messages), err)
	}
}

func TestApplySeedRefusesAnInvalidContact(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := New(database, Config{Seed: Seed{Contacts: []SeedContact{{WaID: "abc"}}}}, Hooks{}, slog.New(slog.DiscardHandler))

	if err := service.ApplySeed(ctx); err == nil {
		t.Fatal("a seed contact with an invalid number was accepted")
	}
}

func TestResetClearsActivityAndKeepsTheSeed(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	media, err := f.service.SaveMedia(ctx, strings.NewReader("%PDF-1.4"), "a.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	f.userSays("hello")
	if _, err := f.service.CreateContact(ctx, "15550000077", "Extra"); err != nil {
		t.Fatal(err)
	}

	if err := f.service.Reset(ctx); err != nil {
		t.Fatal(err)
	}

	contacts, _ := f.store.ListContacts(ctx)
	if len(contacts) != 2 {
		t.Errorf("got %d contacts after reset, want the 2 seeded ones", len(contacts))
	}
	if messages, _ := f.store.ListMessages(ctx, user); len(messages) != 0 {
		t.Errorf("got %d messages after reset", len(messages))
	}
	if _, err := f.store.GetMedia(ctx, media.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("media record survived reset: %v", err)
	}
	if _, err := os.Stat(media.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("media file survived reset: %v", err)
	}
	if page, _ := f.store.ListTemplates(ctx, store.TemplateFilter{Limit: 10}); len(page.Templates) != 3 {
		t.Errorf("templates were cleared: %d left", len(page.Templates))
	}
}

func TestDefaultContactName(t *testing.T) {
	for waID, want := range map[string]string{"15550001234": "User 1234", "123": "User 123"} {
		if got := defaultContactName(waID); got != want {
			t.Errorf("defaultContactName(%q) = %q, want %q", waID, got, want)
		}
	}
}
