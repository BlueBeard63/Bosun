package outboxmod_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmemmod"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/outboxmod"
	"github.com/bluebeard63/bosun/modules/repomod"
	"github.com/bluebeard63/bosun/registry"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	outboxmod.For()
	eventmemmod.Default()
}

func setup(t *testing.T) *bosun.App {
	t.Helper()
	return start(t, memDB(t), &outboxmod.Options{Poll: 20 * time.Millisecond, Batch: 50}, nil)
}

var dbSeq atomic.Int64

// memDB opens a migrated, uniquely named in-memory DB, which keeps tests
// isolated, also across -count runs (a plain :memory: DSN would give each
// pooled connection its own database).
func memDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + strconv.FormatInt(dbSeq.Add(1), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&outboxmod.Record{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// start boots an app with its own relay on db. A non-nil pub replaces the
// in-memory bus as the relay's publisher.
func start(t *testing.T, db *gorm.DB, opts *outboxmod.Options, pub eventmod.Publisher) *bosun.App {
	t.Helper()
	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, db)
	registry.RegisterInstance[*outboxmod.Options](app.Reg, opts)
	if pub != nil {
		registry.RegisterInstance[eventmod.Publisher](app.Reg, pub)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

func resolve[T any](t *testing.T, app *bosun.App) T {
	t.Helper()
	v, err := registry.Resolve[T](app.Reg)
	if err != nil {
		t.Fatalf("resolve %T: %v", *new(T), err)
	}
	return v
}

func TestCommitPublishes(t *testing.T) {
	app := setup(t)
	ob := resolve[outboxmod.Outbox](t, app)
	repo := resolve[repomod.Repo[outboxmod.Record]](t, app)
	sub := resolve[eventmod.Subscriber](t, app)

	got := make(chan []byte, 1)
	if _, err := sub.Subscribe(context.Background(), "orders.placed", func(_ context.Context, d eventmod.Delivery) error {
		got <- d.Message().Data
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	err := repo.Tx(context.Background(), func(ctx context.Context) error {
		return ob.Enqueue(ctx, "orders.placed", []byte(`{"id":1}`), nil)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	select {
	case data := <-got:
		if string(data) != `{"id":1}` {
			t.Fatalf("payload = %s", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed event was never relayed")
	}
}

func TestRollbackDoesNotPublish(t *testing.T) {
	app := setup(t)
	ob := resolve[outboxmod.Outbox](t, app)
	repo := resolve[repomod.Repo[outboxmod.Record]](t, app)
	sub := resolve[eventmod.Subscriber](t, app)

	var delivered atomic.Int64
	if _, err := sub.Subscribe(context.Background(), "orders.rolledback", func(_ context.Context, d eventmod.Delivery) error {
		delivered.Add(1)
		return d.Ack(context.Background())
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	boom := errors.New("boom")
	err := repo.Tx(context.Background(), func(ctx context.Context) error {
		if err := ob.Enqueue(ctx, "orders.rolledback", []byte(`{"id":2}`), nil); err != nil {
			return err
		}
		return boom // roll back
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected rollback error, got %v", err)
	}

	time.Sleep(200 * time.Millisecond) // give the relay several ticks
	if n := delivered.Load(); n != 0 {
		t.Fatalf("rolled-back event was published %d times", n)
	}
	// and no row should remain
	count, err := repo.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("outbox has %d rows after rollback, want 0", count)
	}
}

// stubPub records each publish by outbox row id and fails the subjects in
// fail. If block is set, Publish signals entered and then waits on block,
// ignoring ctx, to simulate a hung broker.
type stubPub struct {
	fail    map[string]bool
	block   chan struct{}
	entered chan struct{}
	sent    chan string // subjects published successfully, if set

	mu    sync.Mutex
	calls map[string]int // by outbox row id, failures included
}

func (p *stubPub) Publish(_ context.Context, subject string, _ []byte, opts ...eventmod.PubOption) error {
	id := eventmod.ResolvePub(opts).Headers[outboxmod.HeaderID]
	p.mu.Lock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[id]++
	p.mu.Unlock()
	if p.block != nil {
		p.entered <- struct{}{}
		<-p.block
	}
	if p.fail[subject] {
		return errors.New("broker rejected " + subject)
	}
	if p.sent != nil {
		p.sent <- subject
	}
	return nil
}

func (p *stubPub) callsFor(id uint) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[strconv.FormatUint(uint64(id), 10)]
}

func enqueue(t *testing.T, ob outboxmod.Outbox, subjects ...string) {
	t.Helper()
	for _, s := range subjects {
		if err := ob.Enqueue(context.Background(), s, []byte(s), nil); err != nil {
			t.Fatalf("enqueue %s: %v", s, err)
		}
	}
}

// waitFor polls cond until it holds or a generous deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFailingRowsDoNotBlockNewer(t *testing.T) {
	pub := &stubPub{fail: map[string]bool{"bad": true}, sent: make(chan string, 1)}
	db := memDB(t)
	app := start(t, db, &outboxmod.Options{
		Poll: 10 * time.Millisecond, Batch: 3, RetryBase: time.Hour, MaxAttempts: 5,
	}, pub)

	// More failing rows than a batch holds, all ahead of the good one.
	enqueue(t, resolve[outboxmod.Outbox](t, app), "bad", "bad", "bad", "bad", "bad", "bad", "good")

	select {
	case s := <-pub.sent:
		if s != "good" {
			t.Fatalf("published %q, want good", s)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a newer row stayed blocked behind failing rows")
	}

	var bad []outboxmod.Record
	if err := db.Where("subject = ?", "bad").Find(&bad).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range bad {
		if r.Attempts != 1 || r.LastError == "" || r.PublishedAt != nil || r.DeadAt != nil {
			t.Fatalf("failed row %d: attempts %d, error %q, published %v, dead %v; want one recorded failure",
				r.ID, r.Attempts, r.LastError, r.PublishedAt, r.DeadAt)
		}
		if r.NextAttemptAt == nil || time.Until(*r.NextAttemptAt) < 30*time.Minute {
			t.Fatalf("failed row %d next attempt = %v, want backed off by RetryBase", r.ID, r.NextAttemptAt)
		}
	}
}

func TestRowDiesAfterMaxAttempts(t *testing.T) {
	pub := &stubPub{fail: map[string]bool{"bad": true}}
	db := memDB(t)
	app := start(t, db, &outboxmod.Options{
		Poll: 10 * time.Millisecond, RetryBase: time.Millisecond, RetryMax: time.Millisecond, MaxAttempts: 3,
	}, pub)
	enqueue(t, resolve[outboxmod.Outbox](t, app), "bad")

	var rec outboxmod.Record
	waitFor(t, "the row to be marked dead", func() bool {
		return db.First(&rec).Error == nil && rec.DeadAt != nil
	})
	if rec.Attempts != 3 || rec.PublishedAt != nil {
		t.Fatalf("dead row: attempts %d, published %v; want 3 attempts, unpublished", rec.Attempts, rec.PublishedAt)
	}
	time.Sleep(100 * time.Millisecond) // several more ticks
	if n := pub.callsFor(rec.ID); n != 3 {
		t.Fatalf("dead row published %d times, want 3 (no retries once dead)", n)
	}
}

func TestCloseReturnsWhenPublishHangs(t *testing.T) {
	pub := &stubPub{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	app := start(t, memDB(t), &outboxmod.Options{Poll: 10 * time.Millisecond, CloseTimeout: 50 * time.Millisecond}, pub)
	enqueue(t, resolve[outboxmod.Outbox](t, app), "stuck")
	select {
	case <-pub.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("relay never published")
	}
	t.Cleanup(func() { close(pub.block) }) // let the abandoned publish finish

	done := make(chan error, 1)
	go func() { done <- app.Shutdown() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Shutdown = nil, want an error reporting the relay did not stop")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Shutdown hung behind a blocked Publish")
	}
}

// testTwoRelays runs two apps, each with its own relay and connection pool,
// against one database, and checks every row is published exactly once.
func testTwoRelays(t *testing.T, open func() *gorm.DB) {
	const rows = 300
	db := open()
	recs := make([]outboxmod.Record, rows)
	for i := range recs {
		recs[i] = outboxmod.Record{Subject: "two.relays", Payload: []byte("x"), CreatedAt: time.Now()}
	}
	if err := db.CreateInBatches(recs, 100).Error; err != nil {
		t.Fatal(err)
	}

	opts := &outboxmod.Options{Poll: 5 * time.Millisecond, Batch: 10}
	pubs := []*stubPub{{}, {}}
	start(t, db, opts, pubs[0])
	start(t, open(), opts, pubs[1])

	waitFor(t, "every row to be published", func() bool {
		var n int64
		return db.Model(&outboxmod.Record{}).Where("published_at IS NOT NULL").Count(&n).Error == nil && n == rows
	})
	var total [2]int
	for _, r := range recs {
		a, b := pubs[0].callsFor(r.ID), pubs[1].callsFor(r.ID)
		if a+b != 1 {
			t.Fatalf("row %d published %d+%d times, want once", r.ID, a, b)
		}
		total[0] += a
		total[1] += b
	}
	t.Logf("relay A published %d rows, relay B %d", total[0], total[1])
}

func TestTwoRelaysPublishEachRowOnce(t *testing.T) {
	// A file database: shared-cache memory databases fail concurrent writers
	// at once instead of waiting out busy_timeout.
	path := filepath.Join(t.TempDir(), "outbox.db")
	testTwoRelays(t, func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1) // one connection per pool, so the pragmas stick
		t.Cleanup(func() { _ = sqlDB.Close() })
		for _, pragma := range []string{"PRAGMA busy_timeout = 10000", "PRAGMA journal_mode = WAL"} {
			if err := db.Exec(pragma).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.AutoMigrate(&outboxmod.Record{}); err != nil {
			t.Fatal(err)
		}
		return db
	})
}

// TestTwoRelaysPostgres runs against the database in BOSUN_POSTGRES_DSN, which
// the CI integration job provides.
func TestTwoRelaysPostgres(t *testing.T) {
	dsn := os.Getenv("BOSUN_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BOSUN_POSTGRES_DSN not set")
	}
	fresh := true
	testTwoRelays(t, func() *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if sqlDB, err := db.DB(); err == nil {
			t.Cleanup(func() { _ = sqlDB.Close() })
		}
		if fresh { // start from an empty table
			fresh = false
			if err := db.Migrator().DropTable(&outboxmod.Record{}); err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&outboxmod.Record{}); err != nil {
				t.Fatal(err)
			}
		}
		return db
	})
}
