package domain

import (
	"testing"
	"time"
)

func TestAdvance(t *testing.T) {
	tests := []struct {
		name    string
		current MessageStatus
		next    MessageStatus
		want    MessageStatus
	}{
		{name: "accepted moves to sent", current: StatusAccepted, next: StatusSent, want: StatusSent},
		{name: "sent moves to delivered", current: StatusSent, next: StatusDelivered, want: StatusDelivered},
		{name: "read can skip delivered", current: StatusSent, next: StatusRead, want: StatusRead},
		{name: "a late delivered does not undo read", current: StatusRead, next: StatusDelivered, want: StatusRead},
		{name: "a late sent does not undo delivered", current: StatusDelivered, next: StatusSent, want: StatusDelivered},
		{name: "a repeated status is a no-op", current: StatusDelivered, next: StatusDelivered, want: StatusDelivered},
		{name: "failed overrides delivered", current: StatusDelivered, next: StatusFailed, want: StatusFailed},
		{name: "failed does not rewrite a read message", current: StatusRead, next: StatusFailed, want: StatusRead},
		{name: "failed is terminal", current: StatusFailed, next: StatusRead, want: StatusFailed},
		{name: "received moves to read", current: StatusReceived, next: StatusRead, want: StatusRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.current.Advance(test.next); got != test.want {
				t.Errorf("%s.Advance(%s) = %s, want %s", test.current, test.next, got, test.want)
			}
		})
	}
}

func TestParseOutboundStatus(t *testing.T) {
	for _, value := range []string{"sent", "delivered", "read", "failed"} {
		if status, ok := ParseOutboundStatus(value); !ok || string(status) != value {
			t.Errorf("ParseOutboundStatus(%q) = %q, %v", value, status, ok)
		}
	}
	for _, value := range []string{"", "accepted", "received", "READ", "played"} {
		if _, ok := ParseOutboundStatus(value); ok {
			t.Errorf("ParseOutboundStatus(%q) accepted a status a business message cannot take", value)
		}
	}
}

func TestWindowOpen(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *time.Time {
		value := now.Add(-ago)
		return &value
	}
	tests := []struct {
		name string
		last *time.Time
		want bool
	}{
		{name: "never wrote", last: nil, want: false},
		{name: "wrote a minute ago", last: at(time.Minute), want: true},
		{name: "one second before expiry", last: at(ServiceWindow - time.Second), want: true},
		{name: "exactly 24 hours ago", last: at(ServiceWindow), want: false},
		{name: "two days ago", last: at(48 * time.Hour), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contact := Contact{WaID: "15550000001", LastInboundAt: test.last}
			if got := contact.WindowOpen(now); got != test.want {
				t.Errorf("WindowOpen = %v, want %v", got, test.want)
			}
		})
	}
}
