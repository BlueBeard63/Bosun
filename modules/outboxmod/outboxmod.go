// Package outboxmod implements the transactional outbox pattern over repomod and
// eventmod. Enqueue writes an outbox row using the caller's transaction, so the
// event is persisted atomically with the business change; a background relay
// then publishes pending rows and marks them sent. This guarantees an event is
// published if and only if its transaction committed, at the cost of
// at-least-once delivery, so consumers must be idempotent.
//
//	var _ = outboxmod.For() // wires a GORM-backed outbox table + the relay
//
//	func (s *Orders) Place(ctx context.Context, o Order) error {
//	    return s.Repo.Tx(ctx, func(ctx context.Context) error {
//	        if err := s.Repo.Create(ctx, &o); err != nil {
//	            return err
//	        }
//	        return s.Outbox.Enqueue(ctx, "orders.placed", encode(o), nil)
//	    })
//	}
package outboxmod

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/gormrepomod"
	"github.com/bluebeard63/bosun/modules/repomod"
)

// HeaderID is the message header carrying the outbox row id, for consumer dedupe.
const HeaderID = "Bosun-Outbox-Id"

// Record is one queued event. PublishedAt is nil until the relay sends it.
type Record struct {
	ID          uint `gorm:"primaryKey"`
	Subject     string
	Payload     []byte
	Headers     string
	CreatedAt   time.Time
	PublishedAt *time.Time `gorm:"index"`
}

// TableName keeps the outbox in its own table.
func (Record) TableName() string { return "bosun_outbox" }

// Outbox enqueues events to be published after the current transaction commits.
type Outbox interface {
	Enqueue(ctx context.Context, subject string, data []byte, hdr map[string]string) error
}

type outbox struct {
	Repo repomod.Repo[Record] // injected
}

var _ Outbox = (*outbox)(nil)

// Enqueue writes an outbox row using the transaction on ctx (if any).
func (o *outbox) Enqueue(ctx context.Context, subject string, data []byte, hdr map[string]string) error {
	h, _ := json.Marshal(hdr)
	return o.Repo.Create(ctx, &Record{
		Subject:   subject,
		Payload:   data,
		Headers:   string(h),
		CreatedAt: time.Now(),
	})
}

// Options configures the relay.
type Options struct {
	Poll  time.Duration
	Batch int
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Poll: time.Second, Batch: 100} })

// relay publishes pending outbox rows on a poll loop.
type relay struct {
	Repo repomod.Repo[Record] // injected
	Pub  eventmod.Publisher   // injected
	Opts *Options             // injected
	quit chan struct{}
	done chan struct{}
}

// Init starts the poll loop (services are eagerly constructed at app.Start).
func (r *relay) Init() error {
	r.quit = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop()
	return nil
}

// Close stops the loop.
func (r *relay) Close() error {
	close(r.quit)
	<-r.done
	return nil
}

func (r *relay) loop() {
	defer close(r.done)
	poll := r.Opts.Poll
	if poll <= 0 {
		poll = time.Second
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-r.quit:
			return
		case <-t.C:
			r.flush(context.Background())
		}
	}
}

// Flush publishes all currently pending rows now. Exposed for tests and for a
// synchronous drain; the relay calls it on every tick.
func (r *relay) Flush(ctx context.Context) {
	batch := r.Opts.Batch
	if batch <= 0 {
		batch = 100
	}
	recs, err := r.Repo.Query().Where("published_at IS NULL").Order("id").Limit(batch).All(ctx)
	if err != nil {
		slog.Error("outbox: query pending", "err", err)
		return
	}
	for i := range recs {
		rec := recs[i]
		hdr := map[string]string{}
		if rec.Headers != "" {
			_ = json.Unmarshal([]byte(rec.Headers), &hdr)
		}
		if hdr == nil { // a stored "null" unmarshals to a nil map
			hdr = map[string]string{}
		}
		hdr[HeaderID] = strconv.FormatUint(uint64(rec.ID), 10)
		if err := r.Pub.Publish(ctx, rec.Subject, rec.Payload, eventmod.WithHeaders(hdr)); err != nil {
			slog.Error("outbox: publish", "id", rec.ID, "err", err)
			continue // retry on the next tick
		}
		now := time.Now()
		rec.PublishedAt = &now
		if err := r.Repo.Update(ctx, &rec); err != nil {
			slog.Error("outbox: mark published", "id", rec.ID, "err", err)
		}
	}
}

func (r *relay) flush(ctx context.Context) { r.Flush(ctx) }

// Register wires the outbox and relay against an already-registered
// repomod.Repo[Record]. Use this when you provide the repo yourself.
func Register() struct{} {
	bosun.Service[outbox]()
	bosun.DefaultBind[Outbox, outbox]()
	bosun.Service[relay]()
	return struct{}{}
}

// For wires a GORM-backed outbox table plus the outbox and relay. It is the
// one-line setup for apps using the GORM repo driver.
func For() struct{} {
	gormrepomod.For[Record]()
	return Register()
}
