package eventmemmod

import (
	"context"
	"strings"

	"github.com/bluebeard63/bosun/modules/eventmod"
)

// In-memory request/reply. A responder registers a handler; a request finds a
// matching responder and calls it synchronously in the caller's goroutine.
// Multiple responders on the same subject are load-balanced round-robin.

type responder struct {
	id      uint64
	subject string
	tokens  []string
	h       eventmod.RequestHandler
}

// Respond registers a request handler for subject.
func (b *Bus) Respond(ctx context.Context, subject string, h eventmod.RequestHandler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	_ = eventmod.ResolveSub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, eventmod.ErrClosed
	}
	b.ensureLocked()
	id := b.nextID
	b.nextID++
	r := &responder{id: id, subject: subject, tokens: strings.Split(subject, "."), h: h}
	b.responders = append(b.responders, r)
	b.mu.Unlock()
	return &respHandle{bus: b, r: r}, nil
}

// Request calls one matching responder and returns its reply.
func (b *Bus) Request(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := eventmod.ResolvePub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, eventmod.ErrClosed
	}
	b.ensureLocked()
	target := b.selectResponderLocked(subject)
	b.mu.Unlock()
	if target == nil {
		return nil, eventmod.ErrNoResponder
	}
	msg := eventmod.Message{Subject: subject, Data: data, ID: newID(), Attempt: 1}
	if len(cfg.Headers) > 0 {
		msg.Headers = cloneHeaders(cfg.Headers)
	}
	return target.h(ctx, msg)
}

func (b *Bus) selectResponderLocked(subject string) *responder {
	tokens := strings.Split(subject, ".")
	var matches []*responder
	for _, r := range b.responders {
		if matchTokens(r.tokens, tokens) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	key := subject
	i := b.respRR[key] % len(matches)
	b.respRR[key] = (b.respRR[key] + 1) & 0x3fffffff
	return matches[i]
}

func (b *Bus) removeResponder(r *responder) {
	b.mu.Lock()
	for i, x := range b.responders {
		if x == r {
			b.responders = append(b.responders[:i], b.responders[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
}

type respHandle struct {
	bus *Bus
	r   *responder
}

func (h *respHandle) Unsubscribe() error {
	h.bus.removeResponder(h.r)
	return nil
}
