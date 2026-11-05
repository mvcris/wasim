// Package graphapi serves the fake Graph API a consumer points its WhatsApp
// client at.
package graphapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/sim"
	"github.com/mvcris/wasim/internal/store"
)

const (
	maxRequestBody    = 4 * 1024 * 1024
	defaultPageLimit  = 25
	maximumPageLimit  = 100
	messagingProduct  = "whatsapp"
	templateListField = "message_templates"
)

var versionPattern = regexp.MustCompile(`^v\d+\.\d+$`)

// Handler serves the Graph API routes.
type Handler struct {
	service *sim.Service
	tokens  []string
	logger  *slog.Logger
}

// New returns the Graph API handler. With no tokens configured, any non-empty
// bearer token is accepted.
func New(service *sim.Service, tokens []string, logger *slog.Logger) http.Handler {
	handler := &Handler{service: service, tokens: tokens, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /{version}/{phoneNumberID}/messages", handler.versioned(handler.postMessage))
	mux.HandleFunc("GET /{version}/{businessAccountID}/"+templateListField, handler.versioned(handler.listTemplates))
	mux.HandleFunc("GET /{version}/{objectID}", handler.versioned(handler.getObject))
	mux.HandleFunc("GET "+sim.AttachmentsPath+"{$}", handler.downloadAttachment)
	mux.HandleFunc("/", handler.unsupported)
	return handler.authenticated(mux)
}

// authenticated requires a bearer token on every route, media downloads
// included: Meta fails those too when the token is missing.
func (h *Handler) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.tokenAccepted(bearerToken(r)) {
			h.writeError(w, cloudapi.NewError(cloudapi.CodeAccessToken, ""))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) tokenAccepted(token string) bool {
	if token == "" {
		return false
	}
	return len(h.tokens) == 0 || slices.Contains(h.tokens, token)
}

func bearerToken(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if found && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(token)
	}
	return r.URL.Query().Get("access_token")
}

// versioned accepts any well-formed version. Meta's versions differ in detail
// but not in the routes this emulator serves.
func (h *Handler) versioned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !versionPattern.MatchString(r.PathValue("version")) {
			h.unsupported(w, r)
			return
		}
		next(w, r)
	}
}

func (h *Handler) unsupported(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(r.URL.Path, "/")
	h.writeError(w, cloudapi.NewUnknownObjectError(id))
}

type sendResponse struct {
	MessagingProduct string            `json:"messaging_product"`
	Contacts         []sendContact     `json:"contacts"`
	Messages         []sendMessageInfo `json:"messages"`
}

type sendContact struct {
	Input string `json:"input"`
	WaID  string `json:"wa_id"`
}

type sendMessageInfo struct {
	ID            string `json:"id"`
	MessageStatus string `json:"message_status,omitempty"`
}

func (h *Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	phoneNumberID := r.PathValue("phoneNumberID")
	if phoneNumberID != h.service.Config().Account.PhoneNumberID {
		h.writeError(w, cloudapi.NewUnknownObjectError(phoneNumberID))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		h.writeError(w, cloudapi.NewError(cloudapi.CodeInvalidParameter, "the request body could not be read"))
		return
	}
	var request cloudapi.SendRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		h.writeError(w, cloudapi.NewError(cloudapi.CodeInvalidParameter, "the request body is not valid JSON: "+err.Error()))
		return
	}
	if request.IsMarkAsRead() {
		h.markRead(w, r, request)
		return
	}

	result, err := h.service.Send(r.Context(), request, raw)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	info := sendMessageInfo{ID: result.MessageID}
	if result.Template {
		info.MessageStatus = "accepted"
	}
	h.writeJSON(w, http.StatusOK, sendResponse{
		MessagingProduct: messagingProduct,
		Contacts:         []sendContact{{Input: result.Input, WaID: result.WaID}},
		Messages:         []sendMessageInfo{info},
	})
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request, request cloudapi.SendRequest) {
	if request.MessageID == "" {
		h.writeError(w, cloudapi.NewError(cloudapi.CodeMissingParameter, "message_id is required"))
		return
	}
	if err := h.service.MarkRead(r.Context(), request.MessageID); err != nil {
		h.writeFailure(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

type mediaResponse struct {
	MessagingProduct string `json:"messaging_product"`
	URL              string `json:"url"`
	MimeType         string `json:"mime_type"`
	SHA256           string `json:"sha256"`
	FileSize         string `json:"file_size"`
	ID               string `json:"id"`
}

// getObject resolves an id. Media is the only object type served for now.
func (h *Handler) getObject(w http.ResponseWriter, r *http.Request) {
	media, err := h.service.ResolveMedia(r.Context(), r.PathValue("objectID"))
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, mediaResponse{
		MessagingProduct: messagingProduct,
		URL:              media.URL,
		MimeType:         media.MimeType,
		SHA256:           media.SHA256,
		FileSize:         strconv.FormatInt(media.FileSize, 10),
		ID:               media.ID,
	})
}

// downloadAttachment serves the bytes behind a minted URL. An expired or
// tampered URL answers 404, which Meta documents as the cue to fetch a new one.
func (h *Handler) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	media, err := h.service.OpenAttachment(r.Context(), r.URL.Query())
	if errors.Is(err, domain.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	ServeMediaFile(w, r, media, h.logger)
}

// ServeMediaFile writes a stored file with its real content type.
func ServeMediaFile(w http.ResponseWriter, r *http.Request, media domain.Media, logger *slog.Logger) {
	file, err := os.Open(media.Path)
	if err != nil {
		logger.Error("open media file", "media", media.ID, "error", err)
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", media.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", media.CreatedAt, file)
}

// templateFields is every field the listing accepts. Asking for anything else
// fails the whole request with #100, which real consumers have to survive.
var templateFields = map[string]func(cloudapi.TemplateDefinition) any{
	"id":               func(t cloudapi.TemplateDefinition) any { return t.ID },
	"name":             func(t cloudapi.TemplateDefinition) any { return t.Name },
	"language":         func(t cloudapi.TemplateDefinition) any { return t.Language },
	"status":           func(t cloudapi.TemplateDefinition) any { return t.Status },
	"category":         func(t cloudapi.TemplateDefinition) any { return t.Category },
	"components":       func(t cloudapi.TemplateDefinition) any { return t.Components },
	"parameter_format": func(t cloudapi.TemplateDefinition) any { return t.Format() },
}

var defaultTemplateFields = []string{"name", "components", "language", "status", "category", "parameter_format"}

type templateListResponse struct {
	Data   []map[string]any `json:"data"`
	Paging *pagingResponse  `json:"paging,omitempty"`
}

type pagingResponse struct {
	Cursors  pagingCursors `json:"cursors"`
	Next     string        `json:"next,omitempty"`
	Previous string        `json:"previous,omitempty"`
}

type pagingCursors struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

func (h *Handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	businessAccountID := r.PathValue("businessAccountID")
	if businessAccountID != h.service.Config().Account.BusinessAccountID {
		h.writeError(w, cloudapi.NewUnknownObjectError(businessAccountID))
		return
	}
	query := r.URL.Query()
	fields, apiErr := parseTemplateFields(query.Get("fields"))
	if apiErr != nil {
		h.writeError(w, apiErr)
		return
	}
	filter, apiErr := parseTemplateFilter(query.Get("limit"), query.Get("after"), query.Get("before"))
	if apiErr != nil {
		h.writeError(w, apiErr)
		return
	}
	filter.Name = query.Get("name")
	filter.Status = query.Get("status")

	page, err := h.service.ListTemplates(r.Context(), filter)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	response := templateListResponse{Data: make([]map[string]any, 0, len(page.Templates))}
	for _, template := range page.Templates {
		entry := make(map[string]any, len(fields)+1)
		for _, field := range fields {
			entry[field] = templateFields[field](template)
		}
		// Graph includes the id whatever `fields` asks for.
		entry["id"] = template.ID
		response.Data = append(response.Data, entry)
	}
	if len(page.Templates) > 0 {
		response.Paging = h.templatePaging(r, page)
	}
	h.writeJSON(w, http.StatusOK, response)
}

func parseTemplateFields(raw string) ([]string, *cloudapi.APIError) {
	if raw == "" {
		return defaultTemplateFields, nil
	}
	fields := strings.Split(raw, ",")
	for index, field := range fields {
		fields[index] = strings.TrimSpace(field)
		if _, known := templateFields[fields[index]]; !known {
			return nil, cloudapi.NewError(cloudapi.CodeInvalidParameter, "Tried accessing nonexisting field ("+fields[index]+")")
		}
	}
	return fields, nil
}

func parseTemplateFilter(limit, after, before string) (store.TemplateFilter, *cloudapi.APIError) {
	filter := store.TemplateFilter{Limit: defaultPageLimit}
	if limit != "" {
		value, err := strconv.Atoi(limit)
		if err != nil || value < 1 {
			return filter, cloudapi.NewError(cloudapi.CodeInvalidParameter, "limit must be a positive integer")
		}
		filter.Limit = min(value, maximumPageLimit)
	}
	var err error
	if after != "" {
		if filter.After, err = store.DecodeCursor(after); err != nil {
			return filter, cloudapi.NewError(cloudapi.CodeInvalidParameter, "after is not a valid cursor")
		}
	}
	if before != "" {
		if filter.Before, err = store.DecodeCursor(before); err != nil {
			return filter, cloudapi.NewError(cloudapi.CodeInvalidParameter, "before is not a valid cursor")
		}
	}
	return filter, nil
}

// templatePaging mints absolute URLs against wasim's public address, because
// consumers follow `next` verbatim instead of building it themselves.
func (h *Handler) templatePaging(r *http.Request, page store.TemplatePage) *pagingResponse {
	first, last := store.EncodeCursor(page.FirstSeq), store.EncodeCursor(page.LastSeq)
	paging := &pagingResponse{Cursors: pagingCursors{Before: first, After: last}}
	if page.HasAfter {
		paging.Next = h.pageURL(r, "after", last)
	}
	if page.HasBefore {
		paging.Previous = h.pageURL(r, "before", first)
	}
	return paging
}

func (h *Handler) pageURL(r *http.Request, cursorName, cursor string) string {
	query := r.URL.Query()
	query.Del("after")
	query.Del("before")
	query.Set(cursorName, cursor)
	return h.service.Config().PublicURL + r.URL.Path + "?" + query.Encode()
}

func (h *Handler) writeFailure(w http.ResponseWriter, err error) {
	var apiErr *cloudapi.APIError
	if errors.As(err, &apiErr) {
		h.writeError(w, apiErr)
		return
	}
	h.logger.Error("graph request failed", "error", err)
	h.writeError(w, cloudapi.NewError(cloudapi.CodeUnknown, ""))
}

func (h *Handler) writeError(w http.ResponseWriter, apiErr *cloudapi.APIError) {
	h.writeJSON(w, apiErr.Status(), apiErr.Envelope())
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.logger.Debug("write graph response", "error", err)
	}
}
