// Package domain holds the entities the simulator persists.
package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
)

// ServiceWindow is how long a user message keeps free-form replies allowed.
const ServiceWindow = 24 * time.Hour

// ErrNotFound reports a missing entity.
var ErrNotFound = errors.New("not found")

// Direction says who sent a message.
type Direction string

// The two directions a message can travel.
const (
	// DirectionInbound is a message a simulated user sent to the business.
	DirectionInbound Direction = "inbound"
	// DirectionOutbound is a message the business sent through the Graph API.
	DirectionOutbound Direction = "outbound"
)

// MessageStatus is the delivery state of a message.
type MessageStatus string

// Message statuses. Sent, delivered, read and failed are the values status
// webhooks carry; received and accepted are the emulator's own starting points.
const (
	// StatusReceived is an inbound message the business has not read yet.
	StatusReceived MessageStatus = "received"
	// StatusAccepted is an outbound message the API accepted, before any webhook.
	StatusAccepted MessageStatus = "accepted"
	// StatusSent means the message left the platform.
	StatusSent MessageStatus = "sent"
	// StatusDelivered means the message reached the user's device.
	StatusDelivered MessageStatus = "delivered"
	// StatusRead means the user opened the message.
	StatusRead MessageStatus = "read"
	// StatusFailed means the message will not be delivered.
	StatusFailed MessageStatus = "failed"
)

var statusRank = map[MessageStatus]int{
	StatusReceived:  0,
	StatusAccepted:  0,
	StatusSent:      1,
	StatusDelivered: 2,
	StatusRead:      3,
}

// Advance returns the status after next arrives. Webhooks can land out of
// order, so progress only moves forward. Failed is terminal, and it does not
// rewrite a read message because the user demonstrably saw it.
func (s MessageStatus) Advance(next MessageStatus) MessageStatus {
	if s == StatusFailed {
		return s
	}
	if next == StatusFailed {
		if s == StatusRead {
			return s
		}
		return next
	}
	if statusRank[next] > statusRank[s] {
		return next
	}
	return s
}

// ParseOutboundStatus validates a status a user can apply to a sent message.
func ParseOutboundStatus(value string) (MessageStatus, bool) {
	switch status := MessageStatus(value); status {
	case StatusSent, StatusDelivered, StatusRead, StatusFailed:
		return status, true
	default:
		return "", false
	}
}

// Contact is a simulated WhatsApp user.
type Contact struct {
	WaID          string     `json:"wa_id"`
	Name          string     `json:"name"`
	LastInboundAt *time.Time `json:"last_inbound_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// WindowOpen reports whether free-form messages can reach this contact at now.
func (c Contact) WindowOpen(now time.Time) bool {
	return c.LastInboundAt != nil && now.Sub(*c.LastInboundAt) < ServiceWindow
}

// Message is one message of a conversation.
type Message struct {
	Seq         int64             `json:"seq"`
	ID          string            `json:"id"`
	ContactWaID string            `json:"contact_wa_id"`
	Direction   Direction         `json:"direction"`
	Type        string            `json:"type"`
	Status      MessageStatus     `json:"status"`
	Rendered    cloudapi.Rendered `json:"rendered"`
	Raw         json.RawMessage   `json:"raw"`
	ContextID   string            `json:"context_id,omitempty"`
	MediaID     string            `json:"media_id,omitempty"`
	Category    string            `json:"category,omitempty"`
	ErrorCode   int               `json:"error_code,omitempty"`
	ErrorTitle  string            `json:"error_title,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

// Media is a stored file reachable through the media endpoints.
type Media struct {
	ID        string    `json:"id"`
	MimeType  string    `json:"mime_type"`
	SHA256    string    `json:"sha256"`
	FileSize  int64     `json:"file_size"`
	Filename  string    `json:"filename,omitempty"`
	Path      string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// RuleSticky marks a failure rule that never runs out.
const RuleSticky = -1

// FailureRule makes upcoming sends fail. An empty ContactWaID matches everyone.
type FailureRule struct {
	ID          int64            `json:"id"`
	ContactWaID string           `json:"contact_wa_id,omitempty"`
	Code        int              `json:"code"`
	Channel     cloudapi.Channel `json:"channel"`
	Remaining   int              `json:"remaining"`
	CreatedAt   time.Time        `json:"created_at"`
}

// DeliveryState is where a webhook delivery stands.
type DeliveryState string

// Webhook delivery states.
const (
	// DeliveryPending waits in the outbox for its next attempt.
	DeliveryPending DeliveryState = "pending"
	// DeliveryInFlight is being posted to the consumer right now.
	DeliveryInFlight DeliveryState = "in_flight"
	// DeliveryDelivered was acknowledged with HTTP 200.
	DeliveryDelivered DeliveryState = "delivered"
	// DeliveryFailed ran out of attempts.
	DeliveryFailed DeliveryState = "failed"
	// DeliverySkipped was recorded without sending, because no webhook URL is set.
	DeliverySkipped DeliveryState = "skipped"
)

// Delivery kinds.
const (
	// DeliveryKindMessage carries a user message.
	DeliveryKindMessage = "message"
	// DeliveryKindStatus carries a status update for a business message.
	DeliveryKindStatus = "status"
)

// Delivery is one webhook in the outbox. A status delivery also names the
// status it applies to its message, applied when the webhook goes out so the
// console shows the same timeline the consumer sees.
type Delivery struct {
	ID            int64         `json:"id"`
	Kind          string        `json:"kind"`
	State         DeliveryState `json:"state"`
	Body          []byte        `json:"-"`
	Attempts      int           `json:"attempts"`
	NextAttemptAt time.Time     `json:"next_attempt_at"`
	LastStatus    int           `json:"last_status,omitempty"`
	LastError     string        `json:"last_error,omitempty"`
	MessageID     string        `json:"message_id,omitempty"`
	MessageStatus MessageStatus `json:"message_status,omitempty"`
	ErrorCode     int           `json:"error_code,omitempty"`
	ErrorTitle    string        `json:"error_title,omitempty"`
	BadSignature  bool          `json:"bad_signature,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
}
