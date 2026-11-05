package events

import (
	"testing"
	"time"
)

func received(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func TestPublishSignalsEverySubscriber(t *testing.T) {
	broker := NewBroker()
	first, stopFirst := broker.Subscribe()
	defer stopFirst()
	second, stopSecond := broker.Subscribe()
	defer stopSecond()

	broker.Publish()

	if !received(first) || !received(second) {
		t.Fatal("a subscriber missed the signal")
	}
}

func TestPendingSignalsCoalesce(t *testing.T) {
	broker := NewBroker()
	changes, stop := broker.Subscribe()
	defer stop()

	broker.Publish()
	broker.Publish()
	broker.Publish()

	if !received(changes) {
		t.Fatal("no signal after publishing")
	}
	if received(changes) {
		t.Fatal("several publishes before a read should collapse into one signal")
	}
}

func TestUnsubscribeStopsSignals(t *testing.T) {
	broker := NewBroker()
	changes, stop := broker.Subscribe()
	stop()

	broker.Publish()

	if received(changes) {
		t.Fatal("an ended subscription still received a signal")
	}
}

func TestPublishWithoutSubscribersDoesNotBlock(t *testing.T) {
	done := make(chan struct{})
	go func() {
		NewBroker().Publish()
		close(done)
	}()
	if !received(done) {
		t.Fatal("Publish blocked with no subscribers")
	}
}
