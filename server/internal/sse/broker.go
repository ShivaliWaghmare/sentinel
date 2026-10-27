// Package sse is an in-memory pub/sub broker for agent run events, keyed by
// agent run ID. Events are stored in the database before they are
// published, so a subscriber that misses some (it was too slow, or
// connected late) can always catch up from the store.
package sse

import (
	"encoding/json"
	"sync"
	"time"
)

// bufferSize is how many events a subscriber may fall behind before it is
// dropped. A dropped subscriber's channel is closed; the client reconnects
// with Last-Event-ID and the replay fills the gap.
const bufferSize = 256

// Event is one agent event as sent to clients.
type Event struct {
	Seq  int64           `json:"seq"`
	TS   time.Time       `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Subscription receives a run's events on C until it is unsubscribed or
// dropped (then C is closed).
type Subscription struct {
	C     <-chan Event
	c     chan Event
	runID int64
}

// Broker fans events out to subscribers. The zero value is not usable; use
// NewBroker.
type Broker struct {
	mu   sync.Mutex
	subs map[int64]map[*Subscription]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: map[int64]map[*Subscription]struct{}{}}
}

// Subscribe starts receiving events for runID.
func (b *Broker) Subscribe(runID int64) *Subscription {
	c := make(chan Event, bufferSize)
	sub := &Subscription{C: c, c: c, runID: runID}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[runID] == nil {
		b.subs[runID] = map[*Subscription]struct{}{}
	}
	b.subs[runID][sub] = struct{}{}
	return sub
}

// Unsubscribe stops delivery and closes sub.C. Calling it twice is safe.
func (b *Broker) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.remove(sub)
}

// Publish delivers ev to every subscriber of runID without blocking. A
// subscriber whose buffer is full is dropped.
func (b *Broker) Publish(runID int64, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs[runID] {
		select {
		case sub.c <- ev:
		default:
			b.remove(sub)
		}
	}
}

// Subscribers returns how many subscribers runID has (for tests).
func (b *Broker) Subscribers(runID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[runID])
}

// remove must be called with mu held.
func (b *Broker) remove(sub *Subscription) {
	set, ok := b.subs[sub.runID]
	if !ok {
		return
	}
	if _, ok := set[sub]; !ok {
		return
	}
	delete(set, sub)
	close(sub.c)
	if len(set) == 0 {
		delete(b.subs, sub.runID)
	}
}
