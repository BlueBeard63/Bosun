package eventmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Request/reply (RPC) sits alongside publish/subscribe. A Requester sends one
// message and waits for exactly one reply; a Responder registers a handler that
// computes the reply. Drivers implement these only where the transport supports
// it (in-memory, NATS, RabbitMQ). Depend on eventmod.Requester / eventmod.Responder
// the same way you depend on Publisher / Subscriber.

var (
	// ErrNoResponder is returned by a Request with no handler for the subject.
	ErrNoResponder = errors.New("eventmod: no responder for subject")
	// ErrRPCUnsupported is returned by drivers whose transport has no request/reply.
	ErrRPCUnsupported = errors.New("eventmod: request/reply not supported by this driver")
)

// RequestHandler computes a reply for a request message.
type RequestHandler func(ctx context.Context, m Message) ([]byte, error)

// Requester sends a request and waits for a single reply.
type Requester interface {
	Request(ctx context.Context, subject string, data []byte, opts ...PubOption) ([]byte, error)
}

// Responder registers a handler that replies to requests on a subject. A group
// makes responders compete (load-balanced RPC workers).
type Responder interface {
	Respond(ctx context.Context, subject string, h RequestHandler, opts ...SubOption) (Subscription, error)
}

// Call is the typed request side: it marshals req, sends it, and unmarshals the
// reply into Resp. Carrier values (correlation id, tenant) propagate with the request.
func Call[Req, Resp any](ctx context.Context, r Requester, subject string, req Req, opts ...PubOption) (Resp, error) {
	var zero Resp
	data, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("eventmod: marshal request %s: %w", subject, err)
	}
	m := Message{Subject: subject}
	InjectContext(ctx, &m)
	if len(m.Headers) > 0 {
		opts = append([]PubOption{WithHeaders(m.Headers)}, opts...)
	}
	reply, err := r.Request(ctx, subject, data, opts...)
	if err != nil {
		return zero, err
	}
	var resp Resp
	if err := json.Unmarshal(reply, &resp); err != nil {
		return zero, fmt.Errorf("eventmod: unmarshal reply %s: %w", subject, err)
	}
	return resp, nil
}

// OnRequest is the typed responder side: it decodes each request into Req, calls
// fn, and encodes the returned Resp as the reply.
func OnRequest[Req, Resp any](ctx context.Context, r Responder, subject string, fn func(context.Context, Req) (Resp, error), opts ...SubOption) (Subscription, error) {
	return r.Respond(ctx, subject, func(hctx context.Context, m Message) ([]byte, error) {
		hctx = ExtractContext(hctx, m)
		var req Req
		if err := json.Unmarshal(m.Data, &req); err != nil {
			return nil, fmt.Errorf("eventmod: unmarshal request %s: %w", subject, err)
		}
		resp, err := fn(hctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	}, opts...)
}
