// Package events tells the console that simulator state changed.
package events

import "sync"

// Broker is a change signal with many subscribers. It carries no payload: the
// console refetches what it shows, so a missed signal costs nothing as long as
// one more follows it.
type Broker struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[chan struct{}]struct{})}
}

// Subscribe returns a channel signalled on change, and the function that ends
// the subscription.
func (b *Broker) Subscribe() (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		delete(b.subscribers, channel)
		b.mu.Unlock()
	}
}

// Publish signals every subscriber. A subscriber with a signal already pending
// is skipped: it is about to refetch anyway.
func (b *Broker) Publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for channel := range b.subscribers {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}
