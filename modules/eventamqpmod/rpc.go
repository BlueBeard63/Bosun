package eventamqpmod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/bluebeard63/bosun/modules/eventmod"
	amqp "github.com/rabbitmq/amqp091-go"
)

const hdrRPCError = "Bosun-Rpc-Error"

var (
	_ eventmod.Requester = (*Bus)(nil)
	_ eventmod.Responder = (*Bus)(nil)
)

func newCorrID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Request implements the classic RabbitMQ RPC pattern: publish with a reply-to
// queue and a correlation id, then wait for the matching reply. Pass a ctx with
// a deadline to bound the wait.
func (b *Bus) Request(ctx context.Context, subject string, data []byte, opts ...eventmod.PubOption) ([]byte, error) {
	b.mu.Lock()
	closed := b.closed
	conn := b.conn
	ex := b.Opts.Exchange
	b.mu.Unlock()
	if closed {
		return nil, eventmod.ErrClosed
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	defer ch.Close()

	replyQ, err := ch.QueueDeclare("", false, true, true, false, nil) // exclusive reply queue
	if err != nil {
		return nil, err
	}
	deliveries, err := ch.Consume(replyQ.Name, "", true, true, false, false, nil)
	if err != nil {
		return nil, err
	}

	cfg := eventmod.ResolvePub(opts)
	headers := amqp.Table{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	corr := newCorrID()
	if err := ch.PublishWithContext(ctx, ex, translateKey(subject), false, false, amqp.Publishing{
		ContentType:   "application/octet-stream",
		Body:          data,
		Headers:       headers,
		ReplyTo:       replyQ.Name,
		CorrelationId: corr,
	}); err != nil {
		return nil, err
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return nil, errors.New("eventamqpmod: reply channel closed")
			}
			if d.CorrelationId != corr {
				continue
			}
			if s, _ := d.Headers[hdrRPCError].(string); s != "" {
				return nil, errors.New(string(d.Body))
			}
			return d.Body, nil
		}
	}
}

// Respond replies to requests on subject. A group load-balances responders.
func (b *Bus) Respond(ctx context.Context, subject string, h eventmod.RequestHandler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	cfg := eventmod.ResolveSub(opts)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, eventmod.ErrClosed
	}
	conn := b.conn
	b.mu.Unlock()

	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	if b.Opts.Prefetch > 0 {
		if err := ch.Qos(b.Opts.Prefetch, 0, false); err != nil {
			_ = ch.Close()
			return nil, err
		}
	}
	var qname string
	if cfg.Group != "" {
		q, err := ch.QueueDeclare(cfg.Group, true, false, false, false, nil)
		if err != nil {
			_ = ch.Close()
			return nil, err
		}
		qname = q.Name
	} else {
		q, err := ch.QueueDeclare("", false, true, true, false, nil)
		if err != nil {
			_ = ch.Close()
			return nil, err
		}
		qname = q.Name
	}
	if err := ch.QueueBind(qname, translateKey(subject), b.Opts.Exchange, false, nil); err != nil {
		_ = ch.Close()
		return nil, err
	}
	deliveries, err := ch.Consume(qname, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		return nil, err
	}

	s := &subscription{ch: ch, bus: b}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	go func() {
		for m := range deliveries {
			reply, herr := h(context.Background(), toMessage(subject, m))
			if m.ReplyTo != "" {
				pub := amqp.Publishing{ContentType: "application/octet-stream", CorrelationId: m.CorrelationId}
				if herr != nil {
					pub.Headers = amqp.Table{hdrRPCError: "1"}
					pub.Body = []byte(herr.Error())
				} else {
					pub.Body = reply
				}
				_ = ch.PublishWithContext(context.Background(), "", m.ReplyTo, false, false, pub)
			}
			_ = m.Ack(false)
		}
	}()
	return &subHandle{bus: b, sub: s}, nil
}
