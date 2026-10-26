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
//
// The relay claims each batch before publishing it by pushing the rows'
// NextAttemptAt forward by a lease, with a conditional UPDATE that only
// succeeds on rows that are still due. Databases serialize concurrent updates
// to a row, so when several replicas run the relay against one database each
// row is claimed by one of them. A failed publish is retried with exponential
// backoff and the row is marked dead after MaxAttempts, so failing rows never
// hold up newer ones.
package outboxmod

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	// NextAttemptAt is when the row is next due; nil means now. The relay
	// moves it forward by the lease when it claims the row and by the
	// backoff after a failed publish.
	NextAttemptAt *time.Time `gorm:"index"`
	// ClaimToken identifies the relay batch that last claimed the row.
	ClaimToken string `gorm:"size:32;index"`
	// Attempts counts failed publishes; LastError holds the latest failure.
	Attempts  int
	LastError string
	// DeadAt is set once Attempts reaches Options.MaxAttempts; the relay
	// skips dead rows. Clear it to retry the row.
	DeadAt *time.Time
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

// Options configures the relay. Zero fields fall back to their defaults.
type Options struct {
	Poll  time.Duration
	Batch int
	// Lease is how long a claimed batch belongs to one relay before another
	// may take it over (default 1m). Keep it above the time a batch takes to
	// publish, or a slow batch may also be published by another replica.
	Lease time.Duration
	// RetryBase and RetryMax bound the exponential backoff after a failed
	// publish (default 1s doubling up to 5m).
	RetryBase time.Duration
	RetryMax  time.Duration
	// MaxAttempts is the number of failed publishes after which a row is
	// marked dead and no longer retried (default 20).
	MaxAttempts int
	// CloseTimeout bounds how long Close waits for an in-flight batch
	// before cancelling it (default bosun.DefaultShutdownTimeout).
	CloseTimeout time.Duration
}

var _ = bosun.Default[*Options](func() *Options {
	return &Options{
		Poll:         time.Second,
		Batch:        100,
		Lease:        time.Minute,
		RetryBase:    time.Second,
		RetryMax:     5 * time.Minute,
		MaxAttempts:  20,
		CloseTimeout: bosun.DefaultShutdownTimeout,
	}
})

func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// backoff is the delay before the next attempt after the given number of
// failed attempts: RetryBase doubled per prior failure, capped at RetryMax.
func (o *Options) backoff(attempts int) time.Duration {
	d := orDefault(o.RetryBase, time.Second)
	limit := orDefault(o.RetryMax, max(5*time.Minute, d))
	for i := 1; i < attempts && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// relay publishes pending outbox rows on a poll loop.
type relay struct {
	Repo   repomod.Repo[Record] // injected
	Pub    eventmod.Publisher   // injected
	Opts   *Options             // injected
	ctx    context.Context      // cancelled when Close gives up waiting
	cancel context.CancelFunc
	quit   chan struct{}
	done   chan struct{}
}

// Init starts the poll loop (services are eagerly constructed at app.Start).
func (r *relay) Init() error {
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.quit = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop()
	return nil
}

// Close stops the loop. An in-flight batch stops after its current row; if
// that takes longer than CloseTimeout (a hung Publish), the batch's context
// is cancelled and Close returns without waiting further. Rows left claimed
// are retried once their lease expires.
func (r *relay) Close() error {
	defer r.cancel()
	close(r.quit)
	timeout := orDefault(r.Opts.CloseTimeout, bosun.DefaultShutdownTimeout)
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-r.done:
		return nil
	case <-t.C:
		return fmt.Errorf("outbox: relay did not stop within %s", timeout)
	}
}

func (r *relay) loop() {
	defer close(r.done)
	poll := orDefault(r.Opts.Poll, time.Second)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-r.quit:
			return
		case <-t.C:
			r.flush(r.ctx)
		}
	}
}

// stopping reports whether Close has been called.
func (r *relay) stopping() bool {
	select {
	case <-r.quit:
		return true
	default:
		return false
	}
}

// Flush claims and publishes the rows that are currently due. Exposed for
// tests and for a synchronous drain; the relay calls it on every tick.
func (r *relay) Flush(ctx context.Context) {
	recs, err := r.claim(ctx, time.Now().UTC())
	if err != nil {
		slog.Error("outbox: claim pending", "err", err)
		return
	}
	for i := range recs {
		if r.stopping() {
			r.release(ctx, recs[i:])
			return
		}
		r.publish(ctx, &recs[i])
	}
}

func (r *relay) flush(ctx context.Context) { r.Flush(ctx) }

// claim leases up to Batch due rows to this relay and returns them. With the
// GORM repo the lease is one conditional UPDATE, so concurrent relays never
// claim the same row: Postgres and MySQL lock the rows and re-check the
// condition after a competing update commits, and SQLite serializes writers.
// Other repos fall back to a per-row read-then-write, which is only safe with
// a single relay.
func (r *relay) claim(ctx context.Context, now time.Time) ([]Record, error) {
	batch := orDefault(r.Opts.Batch, 100)
	due := "published_at IS NULL AND dead_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= ?)"
	recs, err := r.Repo.Query().Where(due, now).Order("id").Limit(batch).All(ctx)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	until := now.Add(orDefault(r.Opts.Lease, time.Minute))

	g, ok := r.Repo.(*gormrepomod.GormRepo[Record])
	if !ok {
		for i := range recs {
			recs[i].NextAttemptAt = &until
			recs[i].ClaimToken = token
			if err := r.Repo.Update(ctx, &recs[i]); err != nil {
				return nil, err
			}
		}
		return recs, nil
	}

	ids := make([]uint, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	db := g.DB.WithContext(ctx)
	err = db.Model(&Record{}).Where("id IN ?", ids).Where(due, now).
		Updates(map[string]any{"next_attempt_at": until, "claim_token": token}).Error
	if err != nil {
		return nil, err
	}
	recs = recs[:0]
	return recs, db.Where("claim_token = ?", token).Order("id").Find(&recs).Error
}

// publish sends one claimed row and records the outcome on it.
func (r *relay) publish(ctx context.Context, rec *Record) {
	hdr := map[string]string{}
	if rec.Headers != "" {
		_ = json.Unmarshal([]byte(rec.Headers), &hdr)
	}
	if hdr == nil { // a stored "null" unmarshals to a nil map
		hdr = map[string]string{}
	}
	hdr[HeaderID] = strconv.FormatUint(uint64(rec.ID), 10)
	err := r.Pub.Publish(ctx, rec.Subject, rec.Payload, eventmod.WithHeaders(hdr))
	now := time.Now().UTC()
	if err == nil {
		rec.PublishedAt = &now
		if err := r.Repo.Update(ctx, rec); err != nil {
			slog.Error("outbox: mark published", "id", rec.ID, "err", err)
		}
		return
	}

	rec.Attempts++
	rec.LastError = err.Error()
	if rec.Attempts >= orDefault(r.Opts.MaxAttempts, 20) {
		rec.DeadAt = &now
		slog.Error("outbox: publish failed, giving up", "id", rec.ID, "attempts", rec.Attempts, "err", err)
	} else {
		next := now.Add(r.Opts.backoff(rec.Attempts))
		rec.NextAttemptAt = &next
		slog.Error("outbox: publish", "id", rec.ID, "attempts", rec.Attempts, "retry_at", next, "err", err)
	}
	if err := r.Repo.Update(ctx, rec); err != nil {
		slog.Error("outbox: record failure", "id", rec.ID, "err", err)
	}
}

// release hands claimed rows back so another relay can take them without
// waiting for the lease to expire.
func (r *relay) release(ctx context.Context, recs []Record) {
	for i := range recs {
		recs[i].NextAttemptAt = nil
		if err := r.Repo.Update(ctx, &recs[i]); err != nil {
			slog.Error("outbox: release claim", "id", recs[i].ID, "err", err)
		}
	}
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

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
