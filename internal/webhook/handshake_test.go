package webhook_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/webhook"
)

func TestVerifyEndpointReportsTransportFailures(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := "http://" + closed.Addr().String() + "/webhook"
	_ = closed.Close()

	for name, url := range map[string]string{
		"an unparsable URL":     "http://[::1",
		"a refused connection":  unreachable,
		"an unsupported scheme": "ftp://example.com/webhook",
	} {
		t.Run(name, func(t *testing.T) {
			result := webhook.VerifyEndpoint(context.Background(), url, "token")
			if result.Verified || result.Error == "" || result.Challenge == "" {
				t.Errorf("result = %+v", result)
			}
		})
	}
}

func TestVerifyEndpointHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := webhook.VerifyEndpoint(ctx, "http://127.0.0.1:1/webhook", "token")
	if result.Verified || !strings.Contains(result.Error, "canceled") {
		t.Errorf("result = %+v", result)
	}
}

func TestDispatcherDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer redirect.Close()
	fixture := startDispatcher(t, redirect.URL, 1)

	fixture.enqueue(t, domain.Delivery{Body: []byte(`{}`)})
	delivery := fixture.waitFor(t, domain.DeliveryFailed)

	if delivery.LastStatus != http.StatusTemporaryRedirect || followed.Load() {
		t.Errorf("last status %d, followed %v", delivery.LastStatus, followed.Load())
	}
}
