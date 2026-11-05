package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mvcris/wasim/internal/domain"
)

const (
	signatureHeader = "X-Hub-Signature-256"
	// Workers fall back to polling so a delivery scheduled in the future goes
	// out on time without anyone waking them.
	pollInterval = 50 * time.Millisecond
	// Enough for the final bookkeeping write after the run context is canceled.
	shutdownWriteTimeout = 2 * time.Second
	maxResponseDrain     = 64 * 1024
)

// Queue is the outbox the dispatcher drains.
type Queue interface {
	ClaimDelivery(ctx context.Context, now time.Time) (*domain.Delivery, error)
	FinishDelivery(ctx context.Context, id int64, state domain.DeliveryState, status int, lastError string, now time.Time) error
	RetryDelivery(ctx context.Context, id int64, status int, lastError string, next time.Time) error
}

// Config controls delivery.
type Config struct {
	URL         string
	AppSecret   string
	Concurrency int
	MaxAttempts int
	Timeout     time.Duration
	RetryBase   time.Duration
	RetryMax    time.Duration
}

// Dispatcher posts outbox webhooks to the consumer. It runs apart from request
// handling on purpose: consumers call the Graph API back while handling a
// webhook (to download media, for example), so delivery can never hold anything
// a Graph request needs.
type Dispatcher struct {
	queue    Queue
	config   Config
	client   *http.Client
	logger   *slog.Logger
	onChange func()
	wake     chan struct{}
}

// NewDispatcher returns a dispatcher. onChange runs after each state change.
func NewDispatcher(queue Queue, config Config, logger *slog.Logger, onChange func()) *Dispatcher {
	return &Dispatcher{
		queue:  queue,
		config: config,
		client: &http.Client{
			Timeout: config.Timeout,
			// A redirect is an answer other than 200, which Meta treats as a
			// failure. Following it would acknowledge a misconfigured endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		logger:   logger,
		onChange: onChange,
		wake:     make(chan struct{}, 1),
	}
}

// Wake tells idle workers there may be new work.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run delivers webhooks until ctx is canceled. Meta delivers concurrently, so
// consumers must not rely on ordering; several workers reproduce that.
func (d *Dispatcher) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for range d.config.Concurrency {
		workers.Go(func() { d.work(ctx) })
	}
	workers.Wait()
}

func (d *Dispatcher) work(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		delivery, err := d.queue.ClaimDelivery(ctx, time.Now())
		if err != nil {
			if ctx.Err() == nil {
				d.logger.Error("claim webhook delivery", "error", err)
			}
		} else if delivery != nil {
			d.onChange()
			d.deliver(ctx, *delivery)
			d.onChange()
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, delivery domain.Delivery) {
	if d.config.URL == "" {
		d.finish(delivery, domain.DeliverySkipped, 0, "no webhook URL configured")
		return
	}
	status, err := d.post(ctx, delivery)
	switch {
	case err == nil && status == http.StatusOK:
		d.finish(delivery, domain.DeliveryDelivered, status, "")
	case errors.Is(err, context.Canceled):
		// Shutting down: leave it for the next run rather than count a failure.
		d.reschedule(delivery, status, "interrupted by shutdown", time.Now())
	default:
		d.handleFailure(delivery, status, err)
	}
}

func (d *Dispatcher) post(ctx context.Context, delivery domain.Delivery) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.config.URL, bytes.NewReader(delivery.Body))
	if err != nil {
		return 0, fmt.Errorf("build webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "wasim-webhook")
	if signature := d.signature(delivery); signature != "" {
		request.Header.Set(signatureHeader, signature)
	}
	response, err := d.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, maxResponseDrain)
	return response.StatusCode, nil
}

// signature leaves the header out without a secret, except on a copy asked to
// carry a bad signature: that copy must still arrive signed, or a consumer
// that skips verification when the header is missing would pass the test.
func (d *Dispatcher) signature(delivery domain.Delivery) string {
	if delivery.BadSignature {
		return Sign(d.config.AppSecret+"-wrong", delivery.Body)
	}
	if d.config.AppSecret == "" {
		return ""
	}
	return Sign(d.config.AppSecret, delivery.Body)
}

func (d *Dispatcher) handleFailure(delivery domain.Delivery, status int, err error) {
	reason := fmt.Sprintf("consumer answered HTTP %d, only 200 acknowledges a webhook", status)
	if err != nil {
		reason = err.Error()
	}
	if delivery.Attempts >= d.config.MaxAttempts {
		d.finish(delivery, domain.DeliveryFailed, status, reason)
		return
	}
	d.reschedule(delivery, status, reason, time.Now().Add(d.retryDelay(delivery.Attempts)))
}

// retryDelay follows the documented curve: retry immediately, then back off
// exponentially. Meta publishes no intervals and spreads attempts over 7 days,
// so the scale is the emulator's own and short enough for a test run.
func (d *Dispatcher) retryDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return 0
	}
	delay := d.config.RetryBase
	for range attempts - 2 {
		delay *= 2
		if delay >= d.config.RetryMax {
			return d.config.RetryMax
		}
	}
	return min(delay, d.config.RetryMax)
}

func (d *Dispatcher) finish(delivery domain.Delivery, state domain.DeliveryState, status int, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWriteTimeout)
	defer cancel()
	if err := d.queue.FinishDelivery(ctx, delivery.ID, state, status, reason, time.Now()); err != nil {
		d.logger.Error("finish webhook delivery", "delivery", delivery.ID, "error", err)
	}
	d.logger.Debug("webhook finished", "delivery", delivery.ID, "state", state, "status", status)
}

func (d *Dispatcher) reschedule(delivery domain.Delivery, status int, reason string, next time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWriteTimeout)
	defer cancel()
	if err := d.queue.RetryDelivery(ctx, delivery.ID, status, reason, next); err != nil {
		d.logger.Error("reschedule webhook delivery", "delivery", delivery.ID, "error", err)
	}
	d.logger.Debug("webhook retry scheduled", "delivery", delivery.ID, "status", status, "reason", reason)
}
