package sse

import (
	"testing"
)

func TestBrokerDeliversPerRun(t *testing.T) {
	b := NewBroker()
	a1 := b.Subscribe(1)
	a2 := b.Subscribe(1)
	other := b.Subscribe(2)

	b.Publish(1, Event{Seq: 1, Type: "phase"})

	for _, sub := range []*Subscription{a1, a2} {
		select {
		case ev := <-sub.C:
			if ev.Seq != 1 {
				t.Errorf("seq = %d, want 1", ev.Seq)
			}
		default:
			t.Error("subscriber of run 1 got nothing")
		}
	}
	select {
	case ev := <-other.C:
		t.Errorf("subscriber of run 2 got %+v", ev)
	default:
	}
}

func TestBrokerUnsubscribeClosesAndIsIdempotent(t *testing.T) {
	b := NewBroker()
	sub := b.Subscribe(1)
	b.Unsubscribe(sub)
	b.Unsubscribe(sub)

	if _, ok := <-sub.C; ok {
		t.Error("channel still open after Unsubscribe")
	}
	if n := b.Subscribers(1); n != 0 {
		t.Errorf("subscribers = %d, want 0", n)
	}
	b.Publish(1, Event{Seq: 1}) // must not panic on the closed channel
}

func TestBrokerDropsSlowSubscriber(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe(1)
	for i := 0; i < bufferSize+1; i++ {
		b.Publish(1, Event{Seq: int64(i + 1)})
	}

	if n := b.Subscribers(1); n != 0 {
		t.Fatalf("subscribers = %d, want the slow one dropped", n)
	}
	got := 0
	for range slow.C {
		got++
	}
	if got != bufferSize {
		t.Errorf("buffered events = %d, want %d before the channel closed", got, bufferSize)
	}
}
