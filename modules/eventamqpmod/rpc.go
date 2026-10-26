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
// The responder is recorded like a subscription, so a reconnect restores it.
func (b *Bus) Respond(ctx context.Context, subject string, h eventmod.RequestHandler, opts ...eventmod.SubOption) (eventmod.Subscription, error) {
	return b.subscribe(&subscription{bus: b, subject: subject, respond: h, opts: opts})
}

// serveRequests runs s.respond for each request on ch and publishes the reply
// to the request's reply-to queue. It ends when ch closes.
func (s *subscription) serveRequests(ch *amqp.Channel, deliveries <-chan amqp.Delivery) {
	for m := range deliveries {
		reply, herr := s.respond(context.Background(), toMessage(m))
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
}
