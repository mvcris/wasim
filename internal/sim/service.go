// Package sim is the simulator engine: it decides what happens to a message and
// which webhooks result, independent of the HTTP servers in front of it.
package sim

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

// ErrInvalid marks a console request the simulator cannot act on. Graph API
// failures use cloudapi.APIError instead.
var ErrInvalid = errors.New("invalid request")

// Config holds the simulator's behaviour settings.
type Config struct {
	Account            webhook.Account
	PublicURL          string
	MediaDir           string
	WebhookURL         string
	VerifyToken        string
	AutoStatuses       []domain.MessageStatus
	StatusDelay        time.Duration
	EnforceWindow      bool
	WindowErrorChannel cloudapi.Channel
	EscapeUnicode      bool
	Seed               Seed
}

// Hooks connects the engine to the rest of the process.
type Hooks struct {
	// OnChange runs after any state change the console should show.
	OnChange func()
	// OnEnqueue runs after webhooks are added to the outbox.
	OnEnqueue func()
}

// Service is the simulator engine.
type Service struct {
	store  *store.Store
	config Config
	hooks  Hooks
	logger *slog.Logger
	now    func() time.Time
	urlKey []byte
}

// New returns a service. The key signing media URLs lives only in memory: those
// URLs expire within minutes, so a restart invalidating them is harmless.
func New(store *store.Store, config Config, hooks Hooks, logger *slog.Logger) *Service {
	urlKey := make([]byte, 32)
	_, _ = rand.Read(urlKey)
	return &Service{
		store:  store,
		config: config,
		hooks:  hooks,
		logger: logger,
		now:    time.Now,
		urlKey: urlKey,
	}
}

// SetClock replaces the time source. Tests use it to cross the 24h window.
func (s *Service) SetClock(now func() time.Time) {
	s.now = now
}

// Now returns the service's current time. The console uses it so window
// badges agree with what a send would decide.
func (s *Service) Now() time.Time {
	return s.now()
}

// Config returns the settings the service runs with.
func (s *Service) Config() Config {
	return s.config
}

// SendResult is what the send endpoint answers.
type SendResult struct {
	Input     string
	WaID      string
	MessageID string
	Template  bool
}

// Send handles a message from the consumer. raw is the original request body,
// kept so the console can show exactly what the consumer sent.
func (s *Service) Send(ctx context.Context, request cloudapi.SendRequest, raw []byte) (SendResult, error) {
	if apiErr := cloudapi.ValidateSend(request); apiErr != nil {
		return SendResult{}, apiErr
	}
	waID, apiErr := cloudapi.NormalizeRecipient(request.To)
	if apiErr != nil {
		return SendResult{}, apiErr
	}
	if waID == s.config.Account.DisplayPhoneNumber {
		return SendResult{}, cloudapi.NewError(cloudapi.CodeSenderIsRecipient, "")
	}

	now := s.now()
	result := SendResult{
		Input:     request.To,
		WaID:      waID,
		MessageID: cloudapi.NewOutboundWAMID(waID),
		Template:  request.MessageType() == cloudapi.TypeTemplate,
	}

	// A synchronous rejection still has to commit, because consuming a failure
	// rule is part of it. It therefore travels outside the transaction's error.
	var rejected *cloudapi.APIError
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		rendered, category, apiErr := s.renderOutbound(ctx, q, request)
		if apiErr != nil {
			rejected = apiErr
			return nil
		}
		contact, err := s.ensureContact(ctx, q, waID, "", now)
		if err != nil {
			return err
		}
		failure, err := s.pickFailure(ctx, q, contact, request, now)
		if err != nil {
			return err
		}
		if failure != nil && failure.channel == cloudapi.ChannelSync {
			rejected = cloudapi.NewError(failure.code, "")
			return nil
		}

		message := domain.Message{
			ID:          result.MessageID,
			ContactWaID: waID,
			Direction:   domain.DirectionOutbound,
			Type:        request.MessageType(),
			Status:      domain.StatusAccepted,
			Rendered:    rendered,
			Raw:         json.RawMessage(raw),
			Category:    category,
			Timestamp:   now,
		}
		if request.Context != nil {
			message.ContextID = request.Context.MessageID
		}
		if err := q.InsertMessage(ctx, &message); err != nil {
			return err
		}
		return s.scheduleStatuses(ctx, q, message, failure, now)
	})
	if err != nil {
		return SendResult{}, err
	}
	if rejected != nil {
		s.changed()
		return SendResult{}, rejected
	}
	s.enqueued()
	return result, nil
}

func (s *Service) renderOutbound(ctx context.Context, q *store.Queries, request cloudapi.SendRequest) (cloudapi.Rendered, string, *cloudapi.APIError) {
	switch request.MessageType() {
	case cloudapi.TypeText:
		return cloudapi.RenderText(*request.Text), "", nil
	case cloudapi.TypeInteractive:
		return cloudapi.RenderInteractive(*request.Interactive), "", nil
	default:
		return s.renderTemplate(ctx, q, *request.Template)
	}
}

func (s *Service) renderTemplate(ctx context.Context, q *store.Queries, content cloudapi.TemplateContent) (cloudapi.Rendered, string, *cloudapi.APIError) {
	definition, err := q.FindTemplate(ctx, content.Name, content.Language.Code)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && !definition.IsApproved()) {
		return cloudapi.Rendered{}, "", cloudapi.NewError(cloudapi.CodeTemplateNotAvailable, fmt.Sprintf(
			"template name (%s) does not exist in %s or is not approved", content.Name, content.Language.Code,
		))
	}
	if err != nil {
		s.logger.Error("find template", "error", err)
		return cloudapi.Rendered{}, "", cloudapi.NewError(cloudapi.CodeUnknown, "")
	}
	if apiErr := cloudapi.ValidateTemplateParameters(definition, content); apiErr != nil {
		return cloudapi.Rendered{}, "", apiErr
	}
	return cloudapi.RenderTemplate(definition, content), strings.ToLower(definition.Category), nil
}

type sendFailure struct {
	code    int
	channel cloudapi.Channel
}

// pickFailure decides whether this send fails. An injected rule goes first so a
// scenario behaves as configured, even outside the window.
func (s *Service) pickFailure(ctx context.Context, q *store.Queries, contact domain.Contact, request cloudapi.SendRequest, now time.Time) (*sendFailure, error) {
	rule, err := q.ConsumeFailureRule(ctx, contact.WaID)
	if err != nil {
		return nil, err
	}
	if rule != nil {
		return &sendFailure{code: rule.Code, channel: rule.Channel}, nil
	}
	if s.config.EnforceWindow && request.NeedsServiceWindow() && !contact.WindowOpen(now) {
		return &sendFailure{code: cloudapi.CodeOutsideWindow, channel: s.config.WindowErrorChannel}, nil
	}
	return nil, nil
}

func (s *Service) scheduleStatuses(ctx context.Context, q *store.Queries, message domain.Message, failure *sendFailure, now time.Time) error {
	if failure != nil {
		return s.enqueueStatus(ctx, q, message, domain.StatusFailed, failure.code, now.Add(s.config.StatusDelay), now)
	}
	for index, status := range s.config.AutoStatuses {
		at := now.Add(s.config.StatusDelay * time.Duration(index+1))
		if err := s.enqueueStatus(ctx, q, message, status, 0, at, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueueStatus(ctx context.Context, q *store.Queries, message domain.Message, status domain.MessageStatus, code int, at, now time.Time) error {
	payload := webhook.Status{
		ID:          message.ID,
		Status:      string(status),
		Timestamp:   webhook.UnixSeconds(at),
		RecipientID: message.ContactWaID,
	}
	delivery := domain.Delivery{
		Kind:          domain.DeliveryKindStatus,
		NextAttemptAt: at,
		MessageID:     message.ID,
		MessageStatus: status,
		CreatedAt:     now,
	}
	switch status {
	case domain.StatusFailed:
		definition, ok := cloudapi.LookupError(code)
		if !ok {
			return fmt.Errorf("%w: unknown error code %d", ErrInvalid, code)
		}
		payload.Errors = []webhook.StatusError{webhook.NewStatusError(definition.Code, definition.Title, definition.Details)}
		delivery.ErrorCode, delivery.ErrorTitle = definition.Code, definition.Title
	case domain.StatusSent, domain.StatusDelivered:
		// Meta sends pricing on `sent` and on one of `delivered` or `read`.
		payload.Pricing = webhook.NewPricing(message.Category)
	}
	body, err := webhook.Encode(webhook.StatusEnvelope(s.config.Account, payload), s.config.EscapeUnicode)
	if err != nil {
		return err
	}
	delivery.Body = body
	return q.EnqueueDelivery(ctx, delivery)
}

// MarkRead handles the mark-as-read form of the send endpoint.
func (s *Service) MarkRead(ctx context.Context, messageID string) error {
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		message, err := q.GetMessage(ctx, messageID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && message.Direction != domain.DirectionInbound) {
			return cloudapi.NewError(cloudapi.CodeInvalidValue, "message_id does not match a received message")
		}
		if err != nil {
			return err
		}
		return q.MarkInboundRead(ctx, message)
	})
	if err != nil {
		return err
	}
	s.changed()
	return nil
}

// SetStatus sends a status webhook for a message on demand.
func (s *Service) SetStatus(ctx context.Context, messageID string, status domain.MessageStatus, code int) error {
	if status == domain.StatusFailed && code == 0 {
		code = cloudapi.CodeUndeliverable
	}
	now := s.now()
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		message, err := q.GetMessage(ctx, messageID)
		if err != nil {
			return err
		}
		if message.Direction != domain.DirectionOutbound {
			return fmt.Errorf("%w: status webhooks only exist for messages the business sent", ErrInvalid)
		}
		return s.enqueueStatus(ctx, q, message, status, code, now, now)
	})
	if err != nil {
		return err
	}
	s.enqueued()
	return nil
}

// Redeliver queues the exact bytes of a past webhook again. A consumer sees a
// duplicate, which Meta documents as normal. With badSignature the copy carries
// a wrong signature, to prove the consumer rejects it.
func (s *Service) Redeliver(ctx context.Context, id int64, badSignature bool) error {
	now := s.now()
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		original, err := q.GetDelivery(ctx, id)
		if err != nil {
			return err
		}
		return q.EnqueueDelivery(ctx, domain.Delivery{
			Kind:          original.Kind,
			Body:          original.Body,
			NextAttemptAt: now,
			MessageID:     original.MessageID,
			BadSignature:  badSignature,
			CreatedAt:     now,
		})
	})
	if err != nil {
		return err
	}
	s.enqueued()
	return nil
}

// VerifyWebhook runs the GET handshake against the configured callback URL.
func (s *Service) VerifyWebhook(ctx context.Context) (webhook.HandshakeResult, error) {
	if s.config.WebhookURL == "" {
		return webhook.HandshakeResult{}, fmt.Errorf("%w: no webhook URL configured", ErrInvalid)
	}
	return webhook.VerifyEndpoint(ctx, s.config.WebhookURL, s.config.VerifyToken), nil
}

// Contacts, rules and read models.

// ListContacts returns every contact.
func (s *Service) ListContacts(ctx context.Context) ([]domain.Contact, error) {
	return s.store.ListContacts(ctx)
}

// CreateContact adds a simulated user.
func (s *Service) CreateContact(ctx context.Context, phone, name string) (domain.Contact, error) {
	waID, apiErr := cloudapi.NormalizeRecipient(phone)
	if apiErr != nil {
		return domain.Contact{}, fmt.Errorf("%w: %s", ErrInvalid, apiErr.Definition.Details)
	}
	contact, err := s.ensureContact(ctx, s.store.Queries, waID, name, s.now())
	if err != nil {
		return domain.Contact{}, err
	}
	s.changed()
	return contact, nil
}

// ExpireWindow closes a contact's 24h window without waiting a day.
func (s *Service) ExpireWindow(ctx context.Context, waID string) error {
	if err := s.store.ClearWindow(ctx, waID); err != nil {
		return err
	}
	s.changed()
	return nil
}

// ListMessages returns a conversation.
func (s *Service) ListMessages(ctx context.Context, waID string) ([]domain.Message, error) {
	return s.store.ListMessages(ctx, waID)
}

// ListDeliveries returns recent webhooks.
func (s *Service) ListDeliveries(ctx context.Context, limit int) ([]domain.Delivery, error) {
	return s.store.ListDeliveries(ctx, limit)
}

// FailureRuleInput describes a failure to inject.
type FailureRuleInput struct {
	ContactWaID string
	Code        int
	Channel     cloudapi.Channel
	// Times is how many sends fail: 0 means one, domain.RuleSticky means until
	// the rule is deleted.
	Times int
}

// AddFailureRule makes upcoming sends fail. Without a channel it takes the
// code's default.
func (s *Service) AddFailureRule(ctx context.Context, input FailureRuleInput) (domain.FailureRule, error) {
	definition, ok := cloudapi.LookupError(input.Code)
	if !ok {
		return domain.FailureRule{}, fmt.Errorf("%w: unknown error code %d", ErrInvalid, input.Code)
	}
	channel := input.Channel
	if channel == "" {
		channel = definition.DefaultChannel
	}
	if !channel.Valid() {
		return domain.FailureRule{}, fmt.Errorf("%w: channel must be sync or async", ErrInvalid)
	}
	times := input.Times
	if times == 0 {
		times = 1
	}
	if times < domain.RuleSticky {
		return domain.FailureRule{}, fmt.Errorf("%w: times must be positive, or -1 to keep failing", ErrInvalid)
	}
	rule, err := s.store.InsertFailureRule(ctx, domain.FailureRule{
		ContactWaID: input.ContactWaID,
		Code:        input.Code,
		Channel:     channel,
		Remaining:   times,
		CreatedAt:   s.now(),
	})
	if err != nil {
		return domain.FailureRule{}, err
	}
	s.changed()
	return rule, nil
}

// ListFailureRules returns the rules in effect.
func (s *Service) ListFailureRules(ctx context.Context) ([]domain.FailureRule, error) {
	return s.store.ListFailureRules(ctx)
}

// DeleteFailureRule removes a rule.
func (s *Service) DeleteFailureRule(ctx context.Context, id int64) error {
	if err := s.store.DeleteFailureRule(ctx, id); err != nil {
		return err
	}
	s.changed()
	return nil
}

// ListTemplates returns a page of the template catalog.
func (s *Service) ListTemplates(ctx context.Context, filter store.TemplateFilter) (store.TemplatePage, error) {
	return s.store.ListTemplates(ctx, filter)
}

func (s *Service) ensureContact(ctx context.Context, q *store.Queries, waID, name string, now time.Time) (domain.Contact, error) {
	contact, err := q.EnsureContact(ctx, waID, name, now)
	if err != nil {
		return domain.Contact{}, err
	}
	if contact.Name != "" {
		return contact, nil
	}
	// profile.name always exists on a real webhook, so a contact first seen as
	// the target of a send still needs something to report.
	return q.EnsureContact(ctx, waID, defaultContactName(waID), now)
}

func defaultContactName(waID string) string {
	return "User " + waID[max(0, len(waID)-4):]
}

func (s *Service) changed() {
	if s.hooks.OnChange != nil {
		s.hooks.OnChange()
	}
}

func (s *Service) enqueued() {
	s.changed()
	if s.hooks.OnEnqueue != nil {
		s.hooks.OnEnqueue()
	}
}
