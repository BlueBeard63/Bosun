package eventnatsmod

import (
	"context"
	"errors"

	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/nats-io/nats.go"
)

const hdrRPCError = "Bosun-Rpc-Error"

var (
	_ eventmod.Requester = (*Bus)(nil)
	_ eventmod.Responder = (*Bus)(nil)
)

// Request uses NATS's native request/reply. Pass a ctx with a deadline to bound
// the wait.
func (b *Bus) Request(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) ([]byte, error) {
	cfg := eventmod.ResolvePub(opts)
	msg := nats.NewMsg(subject)
	msg.Data = data
	for k, v := range cfg.Headers {
		msg.Header.Set(k, v)
	}
	reply, err := b.nc.RequestMsgWithContext(ctx, msg)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, eventmod.ErrNoResponder
		}
		return nil, err
	}
	if reply.Header.Get(hdrRPCError) != "" {
		return nil, errors.New(string(reply.Data))
	}
	return reply.Data, nil
}

// Respond replies to requests on subject. A group load-balances responders.
func (b *Bus) Respond(ctx context.Context, subject string, h eventmod.RequestHandler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	cb := func(m *nats.Msg) {
		if m.Reply == "" {
			return
		}
		reply, herr := h(context.Background(), toMessage(m))
		rm := nats.NewMsg(m.Reply)
		if herr != nil {
			rm.Header.Set(hdrRPCError, "1")
			rm.Data = []byte(herr.Error())
		} else {
			rm.Data = reply
		}
		_ = b.nc.PublishMsg(rm)
	}
	var (
		sub *nats.Subscription
		err error
	)
	if cfg.Group != "" {
		sub, err = b.nc.QueueSubscribe(subject, cfg.Group, cb)
	} else {
		sub, err = b.nc.Subscribe(subject, cb)
	}
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
	return &subHandle{sub: sub}, nil
}
