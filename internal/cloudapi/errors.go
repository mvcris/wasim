package cloudapi

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

// Channel says how a failure reaches the consumer. Meta's error reference no
// longer states which channel each code uses, so every simulated failure has
// to be able to take either one.
type Channel string

const (
	// ChannelSync fails the send with a Graph API error response.
	ChannelSync Channel = "sync"
	// ChannelAsync accepts the send, then reports a failed status webhook.
	ChannelAsync Channel = "async"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool {
	return c == ChannelSync || c == ChannelAsync
}

// Error codes the emulator raises by name. The catalog below holds the full
// list available to failure injection.
const (
	CodeUnknown              = 1
	CodeInvalidParameter     = 100
	CodeAccessToken          = 190
	CodeThroughput           = 130429
	CodeExperiment           = 130472
	CodeMissingParameter     = 131008
	CodeInvalidValue         = 131009
	CodeSenderIsRecipient    = 131021
	CodeUndeliverable        = 131026
	CodeNotAllowedRecipient  = 131030
	CodeOutsideWindow        = 131047
	CodeEcosystemEngagement  = 131049
	CodeUnsupportedType      = 131051
	CodePairRateLimit        = 131056
	CodeTemplateParamCount   = 132000
	CodeTemplateNotAvailable = 132001
)

// ErrorDefinition describes one Cloud API error code. HTTPStatus and
// DefaultChannel are the emulator's defaults, see docs/errors.md for how they
// were chosen.
type ErrorDefinition struct {
	Code           int     `json:"code"`
	Title          string  `json:"title"`
	Details        string  `json:"details"`
	HTTPStatus     int     `json:"http_status"`
	DefaultChannel Channel `json:"default_channel"`
}

var errorCatalog = []ErrorDefinition{
	{CodeOutsideWindow, "Re-engagement message", "More than 24 hours have passed since the recipient last replied to the sender number.", http.StatusBadRequest, ChannelAsync},
	{CodeUndeliverable, "Message undeliverable", "Unable to deliver message. The recipient may not be a WhatsApp user, may not have accepted the latest terms, or may use an old client version.", http.StatusBadRequest, ChannelAsync},
	{CodeEcosystemEngagement, "This message was not delivered to maintain healthy ecosystem engagement.", "In order to maintain a healthy ecosystem engagement, the message failed to be delivered.", http.StatusBadRequest, ChannelAsync},
	{CodeExperiment, "User's number is part of an experiment", "Failed to send message because this user's phone number is part of an experiment", http.StatusBadRequest, ChannelAsync},
	{CodeTemplateParamCount, "Number of parameters does not match the expected number of params", "The number of variable parameter values included in the request did not match the number of variable parameters defined in the template.", http.StatusBadRequest, ChannelSync},
	{CodeTemplateNotAvailable, "Template name does not exist in the translation", "The template does not exist in the specified language or the template has not been approved.", http.StatusNotFound, ChannelSync},
	{CodeUnsupportedType, "Unsupported message type", "Unsupported message type.", http.StatusBadRequest, ChannelSync},
	{CodePairRateLimit, "(Business Account, Consumer Account) pair rate limit hit", "Too many messages sent from the sender phone number to the same recipient phone number in a short period of time.", http.StatusBadRequest, ChannelSync},
	{CodeThroughput, "Rate limit hit", "Cloud API message throughput has been reached.", http.StatusBadRequest, ChannelSync},
	{CodeNotAllowedRecipient, "Recipient phone number not in allowed list", "Recipient phone number not in the allowed list of the public test number.", http.StatusBadRequest, ChannelSync},
	{CodeInvalidParameter, "Invalid parameter", "The request included one or more unsupported or misspelled parameters.", http.StatusBadRequest, ChannelSync},
	{CodeAccessToken, "Invalid OAuth access token", "Your access token has expired.", http.StatusUnauthorized, ChannelSync},
	{CodeMissingParameter, "Required parameter is missing", "The request is missing a required parameter.", http.StatusBadRequest, ChannelSync},
	{CodeInvalidValue, "Parameter value is not valid", "One or more parameter values are invalid.", http.StatusBadRequest, ChannelSync},
	{CodeSenderIsRecipient, "Recipient cannot be sender", "Sender and recipient phone number is the same.", http.StatusBadRequest, ChannelSync},
	{CodeUnknown, "An unknown error occurred", "An unknown error occurred.", http.StatusInternalServerError, ChannelSync},
}

// ErrorCatalog returns every error code the emulator knows, in display order.
func ErrorCatalog() []ErrorDefinition {
	catalog := make([]ErrorDefinition, len(errorCatalog))
	copy(catalog, errorCatalog)
	return catalog
}

// LookupError returns the definition of a code.
func LookupError(code int) (ErrorDefinition, bool) {
	for _, definition := range errorCatalog {
		if definition.Code == code {
			return definition, true
		}
	}
	return ErrorDefinition{}, false
}

// APIError is a failure that goes back to the consumer as a Graph API response.
type APIError struct {
	Definition ErrorDefinition
	Type       string
	Subcode    int
	// Message overrides the "(#code) title" text. Object lookups use it because
	// Graph words them differently.
	Message string
}

// NewError builds an APIError from the catalog. details replaces the catalog
// text when it is not empty, which lets validation name the offending field.
func NewError(code int, details string) *APIError {
	definition, ok := LookupError(code)
	if !ok {
		definition = ErrorDefinition{
			Code:       code,
			Title:      "Unknown error code",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if details != "" {
		definition.Details = details
	}
	return &APIError{Definition: definition, Type: "OAuthException"}
}

// NewUnknownObjectError is the response for an id that resolves to nothing.
// Meta's docs and captured production traffic disagree here, see docs/errors.md.
func NewUnknownObjectError(id string) *APIError {
	apiError := NewError(CodeInvalidParameter, "")
	apiError.Type = "GraphMethodException"
	apiError.Subcode = 33
	apiError.Message = fmt.Sprintf(
		"Unsupported request. Object with ID '%s' does not exist, cannot be loaded due to missing permissions, or does not support this operation.",
		id,
	)
	apiError.Definition.Details = ""
	return apiError
}

// Error returns the message the Graph API would put in the envelope.
func (e *APIError) Error() string {
	return e.text()
}

// Status is the HTTP status of the synchronous response.
func (e *APIError) Status() int {
	return e.Definition.HTTPStatus
}

func (e *APIError) text() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("(#%d) %s", e.Definition.Code, e.Definition.Title)
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message   string     `json:"message"`
	Type      string     `json:"type"`
	Code      int        `json:"code"`
	ErrorData *errorData `json:"error_data,omitempty"`
	Subcode   int        `json:"error_subcode,omitempty"`
	TraceID   string     `json:"fbtrace_id"`
}

type errorData struct {
	MessagingProduct string `json:"messaging_product"`
	Details          string `json:"details"`
}

// Envelope is the JSON body of the synchronous error response.
func (e *APIError) Envelope() any {
	body := errorBody{
		Message: e.text(),
		Type:    e.Type,
		Code:    e.Definition.Code,
		Subcode: e.Subcode,
		TraceID: newTraceID(),
	}
	if e.Definition.Details != "" {
		body.ErrorData = &errorData{MessagingProduct: "whatsapp", Details: e.Definition.Details}
	}
	return errorEnvelope{Error: body}
}

func newTraceID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return "A" + base64.RawURLEncoding.EncodeToString(raw)[:22]
}
