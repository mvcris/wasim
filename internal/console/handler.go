// Package console serves the control API and the browser UI used to play the
// WhatsApp user. It has no authentication, so it must stay on a trusted network.
package console

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/events"
	"github.com/mvcris/wasim/internal/graphapi"
	"github.com/mvcris/wasim/internal/sim"
)

const (
	maxJSONBody       = 1024 * 1024
	maxUploadBody     = 110 * 1024 * 1024
	maxUploadInMemory = 8 * 1024 * 1024
	deliveryListLimit = 200
	keepAliveInterval = 25 * time.Second
)

//go:embed web
var webFiles embed.FS

// Handler serves the console.
type Handler struct {
	service *sim.Service
	broker  *events.Broker
	logger  *slog.Logger
	version string
}

// New returns the console handler.
func New(service *sim.Service, broker *events.Broker, version string, logger *slog.Logger) (http.Handler, error) {
	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("load console assets: %w", err)
	}
	handler := &Handler{service: service, broker: broker, logger: logger, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", handler.info)
	mux.HandleFunc("GET /api/events", handler.streamEvents)
	mux.HandleFunc("GET /api/contacts", handler.listContacts)
	mux.HandleFunc("POST /api/contacts", handler.createContact)
	mux.HandleFunc("GET /api/contacts/{waID}/messages", handler.listMessages)
	mux.HandleFunc("POST /api/contacts/{waID}/messages", handler.receiveMessage)
	mux.HandleFunc("POST /api/contacts/{waID}/window/expire", handler.expireWindow)
	mux.HandleFunc("POST /api/messages/{id}/status", handler.setStatus)
	mux.HandleFunc("GET /api/media/{id}", handler.getMedia)
	mux.HandleFunc("GET /api/errors", handler.listErrors)
	mux.HandleFunc("GET /api/failure-rules", handler.listFailureRules)
	mux.HandleFunc("POST /api/failure-rules", handler.addFailureRule)
	mux.HandleFunc("DELETE /api/failure-rules/{id}", handler.deleteFailureRule)
	mux.HandleFunc("GET /api/deliveries", handler.listDeliveries)
	mux.HandleFunc("POST /api/deliveries/{id}/redeliver", handler.redeliver)
	mux.HandleFunc("POST /api/webhook/verify", handler.verifyWebhook)
	mux.HandleFunc("POST /api/reset", handler.reset)
	mux.Handle("GET /", http.FileServerFS(static))
	return sameOriginOnly(mux), nil
}

// sameOriginOnly stops a website open in the developer's browser from driving
// this unauthenticated API: multipart forms are "simple" requests that skip
// CORS preflight, so the browser would otherwise send them.
func sameOriginOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && r.Method != http.MethodGet {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host {
				writeError(w, http.StatusForbidden, "cross-origin requests are not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type infoResponse struct {
	Version            string                 `json:"version"`
	BusinessAccountID  string                 `json:"business_account_id"`
	PhoneNumberID      string                 `json:"phone_number_id"`
	DisplayPhoneNumber string                 `json:"display_phone_number"`
	WebhookURL         string                 `json:"webhook_url"`
	GraphURL           string                 `json:"graph_url"`
	EnforceWindow      bool                   `json:"enforce_window"`
	AutoStatuses       []domain.MessageStatus `json:"auto_statuses"`
}

func (h *Handler) info(w http.ResponseWriter, _ *http.Request) {
	config := h.service.Config()
	writeJSON(w, http.StatusOK, infoResponse{
		Version:            h.version,
		BusinessAccountID:  config.Account.BusinessAccountID,
		PhoneNumberID:      config.Account.PhoneNumberID,
		DisplayPhoneNumber: config.Account.DisplayPhoneNumber,
		WebhookURL:         config.WebhookURL,
		GraphURL:           config.PublicURL,
		EnforceWindow:      config.EnforceWindow,
		AutoStatuses:       config.AutoStatuses,
	})
}

// streamEvents tells the page when to refetch, over server-sent events.
func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	changes, unsubscribe := h.broker.Subscribe()
	defer unsubscribe()
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if err := controller.Flush(); err != nil {
		return
	}
	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()
	for {
		var frame string
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			frame = "data: changed\n\n"
		case <-keepAlive.C:
			frame = ": keep-alive\n\n"
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}
}

// contactView adds what only the server can compute without clock skew.
type contactView struct {
	domain.Contact
	WindowOpen bool `json:"window_open"`
}

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := h.service.ListContacts(r.Context())
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	now := h.service.Now()
	views := make([]contactView, 0, len(contacts))
	for _, contact := range contacts {
		views = append(views, contactView{Contact: contact, WindowOpen: contact.WindowOpen(now)})
	}
	writeJSON(w, http.StatusOK, views)
}

func (h *Handler) createContact(w http.ResponseWriter, r *http.Request) {
	var input struct {
		WaID string `json:"wa_id"`
		Name string `json:"name"`
	}
	if !h.decodeJSON(w, r, &input) {
		return
	}
	contact, err := h.service.CreateContact(r.Context(), input.WaID, input.Name)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, contact)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := h.service.ListMessages(r.Context(), r.PathValue("waID"))
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

type inboundRequest struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Name      string `json:"name"`
	ContextID string `json:"context_id"`
	ReplyID   string `json:"reply_id"`
	// HoursAgo back-dates the message, for scenarios that begin outside the 24h
	// window.
	HoursAgo float64 `json:"hours_ago"`
}

func (r inboundRequest) toInput(waID string, now time.Time) sim.InboundInput {
	input := sim.InboundInput{
		WaID:      waID,
		Name:      r.Name,
		Type:      r.Type,
		Text:      r.Text,
		ContextID: r.ContextID,
		ReplyID:   r.ReplyID,
	}
	if r.HoursAgo > 0 {
		at := now.Add(-time.Duration(r.HoursAgo * float64(time.Hour)))
		input.Timestamp = &at
	}
	return input
}

// receiveMessage plays a user action: JSON for text and taps, multipart when a
// file is attached.
func (h *Handler) receiveMessage(w http.ResponseWriter, r *http.Request) {
	waID := r.PathValue("waID")
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var input sim.InboundInput
	if mediaType == "multipart/form-data" {
		parsed, ok := h.receiveUpload(w, r, waID)
		if !ok {
			return
		}
		input = parsed
	} else {
		var request inboundRequest
		if !h.decodeJSON(w, r, &request) {
			return
		}
		input = request.toInput(waID, h.service.Now())
	}
	message, err := h.service.Receive(r.Context(), input)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, message)
}

func (h *Handler) receiveUpload(w http.ResponseWriter, r *http.Request, waID string) (sim.InboundInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	//nolint:gosec // The body is capped by MaxBytesReader on the line above.
	if err := r.ParseMultipartForm(maxUploadInMemory); err != nil {
		writeError(w, http.StatusBadRequest, "could not read the upload: "+err.Error())
		return sim.InboundInput{}, false
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return sim.InboundInput{}, false
	}
	defer file.Close()
	media, err := h.service.SaveMedia(r.Context(), file, header.Filename, header.Header.Get("Content-Type"))
	if err != nil {
		h.writeFailure(w, err)
		return sim.InboundInput{}, false
	}
	return sim.InboundInput{
		WaID:      waID,
		Type:      sim.InboundMedia,
		MediaID:   media.ID,
		Caption:   r.FormValue("caption"),
		ContextID: r.FormValue("context_id"),
	}, true
}

func (h *Handler) expireWindow(w http.ResponseWriter, r *http.Request) {
	if err := h.service.ExpireWindow(r.Context(), r.PathValue("waID")); err != nil {
		h.writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
		Code   int    `json:"code"`
	}
	if !h.decodeJSON(w, r, &input) {
		return
	}
	status, ok := domain.ParseOutboundStatus(input.Status)
	if !ok {
		writeError(w, http.StatusBadRequest, "status must be sent, delivered, read or failed")
		return
	}
	if err := h.service.SetStatus(r.Context(), r.PathValue("id"), status, input.Code); err != nil {
		h.writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) getMedia(w http.ResponseWriter, r *http.Request) {
	media, err := h.service.GetMedia(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	graphapi.ServeMediaFile(w, r, media, h.logger)
}

func (h *Handler) listErrors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, cloudapi.ErrorCatalog())
}

func (h *Handler) listFailureRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.service.ListFailureRules(r.Context())
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *Handler) addFailureRule(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ContactWaID string           `json:"contact_wa_id"`
		Code        int              `json:"code"`
		Channel     cloudapi.Channel `json:"channel"`
		Times       int              `json:"times"`
	}
	if !h.decodeJSON(w, r, &input) {
		return
	}
	rule, err := h.service.AddFailureRule(r.Context(), sim.FailureRuleInput{
		ContactWaID: input.ContactWaID,
		Code:        input.Code,
		Channel:     input.Channel,
		Times:       input.Times,
	})
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) deleteFailureRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteFailureRule(r.Context(), id); err != nil {
		h.writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deliveryView exposes the webhook body as text: it may hold \u escapes that
// decoding would erase, and those bytes are exactly what got signed.
type deliveryView struct {
	domain.Delivery
	Payload string `json:"payload"`
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	deliveries, err := h.service.ListDeliveries(r.Context(), deliveryListLimit)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	views := make([]deliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		views = append(views, deliveryView{Delivery: delivery, Payload: string(delivery.Body)})
	}
	writeJSON(w, http.StatusOK, views)
}

func (h *Handler) redeliver(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		BadSignature bool `json:"bad_signature"`
	}
	if !h.decodeJSON(w, r, &input) {
		return
	}
	if err := h.service.Redeliver(r.Context(), id, input.BadSignature); err != nil {
		h.writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) verifyWebhook(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.VerifyWebhook(r.Context())
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Reset(r.Context()); err != nil {
		h.writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a number")
		return 0, false
	}
	return id, true
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (h *Handler) writeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, sim.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		h.logger.Error("console request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error, see the wasim logs")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
