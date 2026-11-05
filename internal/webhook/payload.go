// Package webhook builds, signs and delivers the webhooks a consumer receives.
package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	objectName      = "whatsapp_business_account"
	fieldMessages   = "messages"
	productName     = "whatsapp"
	pricingModel    = "PMP"
	signaturePrefix = "sha256="
	errorHref       = "/documentation/business-messaging/whatsapp/support/error-codes"
)

// Account identifies the business account a webhook belongs to.
type Account struct {
	BusinessAccountID  string
	PhoneNumberID      string
	DisplayPhoneNumber string
}

// Envelope is the top level of every webhook body.
type Envelope struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry groups changes of one business account.
type Entry struct {
	ID      string   `json:"id"`
	Changes []Change `json:"changes"`
}

// Change is one update.
type Change struct {
	Value Value  `json:"value"`
	Field string `json:"field"`
}

// Value is the payload of an update.
type Value struct {
	MessagingProduct string           `json:"messaging_product"`
	Metadata         Metadata         `json:"metadata"`
	Contacts         []Contact        `json:"contacts,omitempty"`
	Messages         []InboundMessage `json:"messages,omitempty"`
	Statuses         []Status         `json:"statuses,omitempty"`
}

// Metadata names the receiving business number.
type Metadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

// Contact is the sender of an inbound message.
type Contact struct {
	Profile Profile `json:"profile"`
	WaID    string  `json:"wa_id"`
}

// Profile holds the name the user chose for themselves.
type Profile struct {
	Name string `json:"name"`
}

// InboundMessage is a message from a user. Exactly one content field is set,
// matching Type.
type InboundMessage struct {
	Context     *Context     `json:"context,omitempty"`
	From        string       `json:"from"`
	ID          string       `json:"id"`
	Timestamp   string       `json:"timestamp"`
	Type        string       `json:"type"`
	Text        *Text        `json:"text,omitempty"`
	Image       *MediaObject `json:"image,omitempty"`
	Document    *MediaObject `json:"document,omitempty"`
	Interactive *Interactive `json:"interactive,omitempty"`
	Button      *Button      `json:"button,omitempty"`
}

// Context links a message to the one it replies to.
type Context struct {
	From string `json:"from"`
	ID   string `json:"id"`
}

// Text is a text message body.
type Text struct {
	Body string `json:"body"`
}

// MediaObject is an inbound image or document.
type MediaObject struct {
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mime_type"`
	SHA256   string `json:"sha256"`
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
}

// Interactive is a tap on a reply button or a list row.
type Interactive struct {
	Type        string     `json:"type"`
	ButtonReply *ReplyItem `json:"button_reply,omitempty"`
	ListReply   *ReplyItem `json:"list_reply,omitempty"`
}

// ReplyItem is the option a user tapped.
type ReplyItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// Button is a tap on a template's quick-reply button.
type Button struct {
	Payload string `json:"payload"`
	Text    string `json:"text"`
}

// Status reports what happened to a message the business sent. `conversation`
// is left out on purpose: Meta omits it from webhook version v24.0 on.
type Status struct {
	ID          string        `json:"id"`
	Status      string        `json:"status"`
	Timestamp   string        `json:"timestamp"`
	RecipientID string        `json:"recipient_id"`
	Pricing     *Pricing      `json:"pricing,omitempty"`
	Errors      []StatusError `json:"errors,omitempty"`
}

// Pricing describes how a message would be billed.
type Pricing struct {
	Billable     bool   `json:"billable"`
	PricingModel string `json:"pricing_model"`
	Type         string `json:"type"`
	Category     string `json:"category"`
}

// StatusError explains a failed status. Its shape differs from the synchronous
// Graph error envelope.
type StatusError struct {
	Code      int             `json:"code"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	ErrorData StatusErrorData `json:"error_data"`
	Href      string          `json:"href"`
}

// StatusErrorData holds the human-readable reason.
type StatusErrorData struct {
	Details string `json:"details"`
}

// NewStatusError builds the error object of a failed status.
func NewStatusError(code int, title, details string) StatusError {
	return StatusError{
		Code:      code,
		Title:     title,
		Message:   title,
		ErrorData: StatusErrorData{Details: details},
		Href:      errorHref,
	}
}

// NewPricing returns the pricing of a message in the given category. Template
// categories bill; anything else is a service message, free inside the window.
func NewPricing(category string) *Pricing {
	if category == "" {
		return &Pricing{Billable: false, PricingModel: pricingModel, Type: "free_customer_service", Category: "service"}
	}
	return &Pricing{Billable: true, PricingModel: pricingModel, Type: "regular", Category: category}
}

// InboundEnvelope wraps a user message.
func InboundEnvelope(account Account, sender Contact, message InboundMessage) Envelope {
	return envelope(account, Value{
		Contacts: []Contact{sender},
		Messages: []InboundMessage{message},
	})
}

// StatusEnvelope wraps a status update.
func StatusEnvelope(account Account, status Status) Envelope {
	return envelope(account, Value{Statuses: []Status{status}})
}

func envelope(account Account, value Value) Envelope {
	value.MessagingProduct = productName
	value.Metadata = Metadata{
		DisplayPhoneNumber: account.DisplayPhoneNumber,
		PhoneNumberID:      account.PhoneNumberID,
	}
	return Envelope{
		Object: objectName,
		Entry: []Entry{{
			ID:      account.BusinessAccountID,
			Changes: []Change{{Value: value, Field: fieldMessages}},
		}},
	}
}

// UnixSeconds formats a timestamp the way webhooks carry it: a string of Unix
// seconds.
func UnixSeconds(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

// Encode serializes a webhook body. With escapeUnicode, non-ASCII characters
// become \uXXXX with lowercase hex. Meta documents signing that form for
// Messenger, and it makes a consumer that re-serializes JSON before verifying
// fail locally instead of only on the first accented message in production.
func Encode(value any, escapeUnicode bool) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode webhook: %w", err)
	}
	body := bytes.TrimRight(buffer.Bytes(), "\n")
	if !escapeUnicode {
		return body, nil
	}
	return escapeNonASCII(body), nil
}

// escapeNonASCII works on the encoded document because non-ASCII can only
// appear inside JSON strings, where the escape is always valid.
func escapeNonASCII(body []byte) []byte {
	escaped := make([]byte, 0, len(body))
	for len(body) > 0 {
		char, size := utf8.DecodeRune(body)
		switch {
		case char < utf8.RuneSelf:
			escaped = append(escaped, body[0])
		case char >= 0x10000:
			char -= 0x10000
			escaped = fmt.Appendf(escaped, `\u%04x\u%04x`, 0xD800+(char>>10), 0xDC00+(char&0x3FF))
		default:
			escaped = fmt.Appendf(escaped, `\u%04x`, char)
		}
		body = body[size:]
	}
	return escaped
}

// Sign returns the X-Hub-Signature-256 header value for body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
