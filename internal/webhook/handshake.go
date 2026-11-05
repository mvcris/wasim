package webhook

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	handshakeTimeout   = 5 * time.Second
	maxHandshakeAnswer = 1024
)

// HandshakeResult is the outcome of the verification request.
type HandshakeResult struct {
	Verified  bool   `json:"verified"`
	Status    int    `json:"status,omitempty"`
	Challenge string `json:"challenge"`
	Answer    string `json:"answer,omitempty"`
	Error     string `json:"error,omitempty"`
}

// VerifyEndpoint performs the GET handshake Meta runs when a callback URL is
// configured. A consumer passes by answering HTTP 200 with the challenge.
func VerifyEndpoint(ctx context.Context, callbackURL, verifyToken string) HandshakeResult {
	// Digits satisfy both descriptions Meta gives: "a random string" and "an int".
	challenge := newChallenge()
	result := HandshakeResult{Challenge: challenge}

	target, err := url.Parse(callbackURL)
	if err != nil {
		result.Error = fmt.Sprintf("invalid webhook URL: %v", err)
		return result
	}
	query := target.Query()
	query.Set("hub.mode", "subscribe")
	query.Set("hub.challenge", challenge)
	query.Set("hub.verify_token", verifyToken)
	target.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(response.Body, maxHandshakeAnswer))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = response.StatusCode
	result.Answer = string(answer)
	result.Verified = response.StatusCode == http.StatusOK && strings.TrimSpace(result.Answer) == challenge
	if !result.Verified {
		result.Error = "expected HTTP 200 with the challenge as the body"
	}
	return result
}

func newChallenge() string {
	value, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000))
	if err != nil {
		return "1158201444"
	}
	return value.Add(value, big.NewInt(1_000_000_000)).String()
}
