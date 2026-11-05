package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

// Message types a simulated user can produce.
const (
	InboundText  = "text"
	InboundMedia = "media"
	InboundReply = "reply"

	typeInteractive = "interactive"
	typeButton      = "button"
)

// InboundInput is something a simulated user does.
type InboundInput struct {
	WaID string
	// Name updates the contact's profile name when set.
	Name string
	// Type is InboundText, InboundMedia or InboundReply.
	Type string
	Text string
	// MediaID is a file stored with SaveMedia, for InboundMedia.
	MediaID string
	// Caption goes with InboundMedia.
	Caption string
	// ContextID is the wamid being quoted, or the message holding the tapped
	// option for InboundReply.
	ContextID string
	// ReplyID is the id of the tapped button or list row.
	ReplyID string
	// Timestamp back-dates the message, which is how a scenario starts with an
	// already expired window.
	Timestamp *time.Time
}

// Receive records a user message and queues its webhook.
func (s *Service) Receive(ctx context.Context, input InboundInput) (domain.Message, error) {
	now := s.now()
	at := now
	if input.Timestamp != nil {
		at = *input.Timestamp
	}
	var message domain.Message
	err := s.store.InTx(ctx, func(q *store.Queries) error {
		contact, err := s.ensureContact(ctx, q, input.WaID, input.Name, now)
		if err != nil {
			return err
		}
		id := cloudapi.NewInboundWAMID(contact.WaID)
		built, err := s.buildInbound(ctx, q, contact, id, at, input)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(built.payload)
		if err != nil {
			return fmt.Errorf("encode inbound message: %w", err)
		}
		message = domain.Message{
			ID:          id,
			ContactWaID: contact.WaID,
			Direction:   domain.DirectionInbound,
			Type:        built.payload.Type,
			Status:      domain.StatusReceived,
			Rendered:    built.rendered,
			Raw:         raw,
			ContextID:   input.ContextID,
			MediaID:     input.MediaID,
			Timestamp:   at,
		}
		if err := q.InsertMessage(ctx, &message); err != nil {
			return err
		}
		if err := q.TouchInbound(ctx, contact.WaID, at); err != nil {
			return err
		}
		sender := webhook.Contact{Profile: webhook.Profile{Name: contact.Name}, WaID: contact.WaID}
		body, err := webhook.Encode(webhook.InboundEnvelope(s.config.Account, sender, built.payload), s.config.EscapeUnicode)
		if err != nil {
			return err
		}
		return q.EnqueueDelivery(ctx, domain.Delivery{
			Kind:          domain.DeliveryKindMessage,
			Body:          body,
			NextAttemptAt: now,
			MessageID:     id,
			CreatedAt:     now,
		})
	})
	if err != nil {
		return domain.Message{}, err
	}
	s.enqueued()
	return message, nil
}

type builtInbound struct {
	payload  webhook.InboundMessage
	rendered cloudapi.Rendered
}

func (s *Service) buildInbound(ctx context.Context, q *store.Queries, contact domain.Contact, id string, at time.Time, input InboundInput) (builtInbound, error) {
	built := builtInbound{payload: webhook.InboundMessage{
		From:      contact.WaID,
		ID:        id,
		Timestamp: webhook.UnixSeconds(at),
	}}
	var quoted *domain.Message
	if input.ContextID != "" {
		message, err := q.GetMessage(ctx, input.ContextID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && message.ContactWaID != contact.WaID) {
			return builtInbound{}, fmt.Errorf("%w: the quoted message is not part of this conversation", ErrInvalid)
		}
		if err != nil {
			return builtInbound{}, err
		}
		quoted = &message
		built.payload.Context = &webhook.Context{From: s.quotedSender(message), ID: message.ID}
	}

	switch input.Type {
	case InboundText:
		return applyText(built, input)
	case InboundMedia:
		return s.applyMedia(ctx, q, built, input)
	case InboundReply:
		return applyReply(built, quoted, input)
	default:
		return builtInbound{}, fmt.Errorf("%w: unknown message type %q", ErrInvalid, input.Type)
	}
}

func (s *Service) quotedSender(message domain.Message) string {
	if message.Direction == domain.DirectionOutbound {
		return s.config.Account.DisplayPhoneNumber
	}
	return message.ContactWaID
}

func applyText(built builtInbound, input InboundInput) (builtInbound, error) {
	if input.Text == "" {
		return builtInbound{}, fmt.Errorf("%w: text is required", ErrInvalid)
	}
	built.payload.Type = cloudapi.TypeText
	built.payload.Text = &webhook.Text{Body: input.Text}
	built.rendered = cloudapi.Rendered{Body: input.Text}
	return built, nil
}

func (s *Service) applyMedia(ctx context.Context, q *store.Queries, built builtInbound, input InboundInput) (builtInbound, error) {
	media, err := q.GetMedia(ctx, input.MediaID)
	if errors.Is(err, domain.ErrNotFound) {
		return builtInbound{}, fmt.Errorf("%w: unknown media", ErrInvalid)
	}
	if err != nil {
		return builtInbound{}, err
	}
	rule, err := cloudapi.LookupMediaRule(media.MimeType)
	if err != nil {
		return builtInbound{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	object := &webhook.MediaObject{
		Caption:  input.Caption,
		MimeType: media.MimeType,
		SHA256:   media.SHA256,
		ID:       media.ID,
		URL:      s.mediaURL(media.ID),
	}
	built.payload.Type = rule.Kind
	if rule.Kind == cloudapi.MediaImage {
		built.payload.Image = object
	} else {
		object.Filename = media.Filename
		built.payload.Document = object
	}
	built.rendered = cloudapi.Rendered{Body: input.Caption}
	return built, nil
}

func applyReply(built builtInbound, quoted *domain.Message, input InboundInput) (builtInbound, error) {
	if quoted == nil || quoted.Direction != domain.DirectionOutbound {
		return builtInbound{}, fmt.Errorf("%w: a reply must point at a message the business sent", ErrInvalid)
	}
	if button, ok := quoted.Rendered.FindButton(input.ReplyID); ok {
		built.rendered = cloudapi.Rendered{Body: button.Title}
		if button.Kind == cloudapi.ButtonKindQuickReply {
			built.payload.Type = typeButton
			built.payload.Button = &webhook.Button{Payload: button.ID, Text: button.Title}
			return built, nil
		}
		built.payload.Type = typeInteractive
		built.payload.Interactive = &webhook.Interactive{
			Type:        "button_reply",
			ButtonReply: &webhook.ReplyItem{ID: button.ID, Title: button.Title},
		}
		return built, nil
	}
	if row, ok := quoted.Rendered.FindRow(input.ReplyID); ok {
		built.rendered = cloudapi.Rendered{Body: row.Title}
		built.payload.Type = typeInteractive
		built.payload.Interactive = &webhook.Interactive{
			Type:      "list_reply",
			ListReply: &webhook.ReplyItem{ID: row.ID, Title: row.Title, Description: row.Description},
		}
		return built, nil
	}
	return builtInbound{}, fmt.Errorf("%w: that message has no option with id %q", ErrInvalid, input.ReplyID)
}
