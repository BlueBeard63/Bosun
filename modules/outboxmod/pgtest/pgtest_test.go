package pgtest_test

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/eventmod"
	"github.com/bluebeard63/bosun/modules/outboxmod"
	"github.com/bluebeard63/bosun/registry"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func init() {
	outboxmod.For()
}

// countingPub records each publish by outbox row id.
type countingPub struct {
	mu    sync.Mutex
	calls map[string]int
}

func (p *countingPub) Publish(_ context.Context, _ string, _ []byte, opts ...eventmod.PubOption) error {
	id := eventmod.ResolvePub(opts).Headers[outboxmod.HeaderID]
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[id]++
	return nil
}

func (p *countingPub) callsFor(id uint) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[strconv.FormatUint(uint64(id), 10)]
}

// start boots an app whose relay runs on db and publishes to pub.
func start(t *testing.T, db *gorm.DB, opts *outboxmod.Options, pub eventmod.Publisher) {
	t.Helper()
	app := bosun.New()
	registry.RegisterInstance[*gorm.DB](app.Reg, db)
	registry.RegisterInstance[*outboxmod.Options](app.Reg, opts)
	registry.RegisterInstance[eventmod.Publisher](app.Reg, pub)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
}

// TestTwoRelaysPublishEachRowOnce runs two apps, each with its own relay and
// connection pool, against the database in BOSUN_POSTGRES_DSN and checks
// every row is published exactly once.
func TestTwoRelaysPublishEachRowOnce(t *testing.T) {
	dsn := os.Getenv("BOSUN_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BOSUN_POSTGRES_DSN not set")
	}
	open := func() *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if sqlDB, err := db.DB(); err == nil {
			t.Cleanup(func() { _ = sqlDB.Close() })
		}
		return db
	}

	const rows = 300
	db := open()
	// start from an empty table
	if err := db.Migrator().DropTable(&outboxmod.Record{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&outboxmod.Record{}); err != nil {
		t.Fatal(err)
	}
	recs := make([]outboxmod.Record, rows)
	for i := range recs {
		recs[i] = outboxmod.Record{Subject: "two.relays", Payload: []byte("x"), CreatedAt: time.Now()}
	}
	if err := db.CreateInBatches(recs, 100).Error; err != nil {
		t.Fatal(err)
	}

	opts := &outboxmod.Options{Poll: 5 * time.Millisecond, Batch: 10}
	pubs := []*countingPub{{}, {}}
	start(t, db, opts, pubs[0])
	start(t, open(), opts, pubs[1])

	deadline := time.Now().Add(30 * time.Second)
	for {
		var n int64
		if db.Model(&outboxmod.Record{}).Where("published_at IS NOT NULL").Count(&n).Error == nil && n == rows {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for every row to be published (%d of %d)", n, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
