package http

import (
	"sync"

	"legos-factory/backend/internal/model"
)

// Bus is a tiny in-process pub/sub used to fan work-order
// events out to SSE subscribers. Persistence happens in the repository;
// the bus is delivery-only.
type Bus struct {
	mu   sync.Mutex
	subs map[string][]chan model.LiveEvent
}

func NewBus() *Bus {
	return &Bus{subs: map[string][]chan model.LiveEvent{}}
}

func (b *Bus) Subscribe(woID string) (<-chan model.LiveEvent, func()) {
	ch := make(chan model.LiveEvent, 64)
	b.mu.Lock()
	b.subs[woID] = append(b.subs[woID], ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subs[woID]
		for i, c := range list {
			if c == ch {
				b.subs[woID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		close(ch)
	}
}

// SubscribeUser receives the events published for one user (review runs, CLI login).
func (b *Bus) SubscribeUser(userID string) (<-chan model.LiveEvent, func()) {
	return b.Subscribe("user:" + userID)
}

// Publish delivers to subscribers of the WO and to "*" (global listeners); an event
// with a UserID reaches only that user's subscribers.
// Slow subscribers drop events (channel is buffered); SSE catches up from
// the persisted event log.
func (b *Bus) Publish(e model.LiveEvent) {
	keys := []string{e.WOID, "*"}
	if e.UserID != "" {
		keys = []string{"user:" + e.UserID}
	}
	for _, key := range keys {
		b.mu.Lock()
		subs := append([]chan model.LiveEvent(nil), b.subs[key]...)
		b.mu.Unlock()
		for _, ch := range subs {
			select {
			case ch <- e:
			default:
			}
		}
	}
}
