package config

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

// memSource is a mutable in-memory Source.
type memSource struct {
	mu  sync.Mutex
	doc map[string]json.RawMessage
}

func (m *memSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]json.RawMessage{}
	for k, v := range m.doc {
		out[k] = v
	}
	return out, nil
}

func (m *memSource) set(doc map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doc = map[string]json.RawMessage{}
	for k, v := range doc {
		m.doc[k] = json.RawMessage(v)
	}
}

func newMemSource(doc map[string]string) *memSource {
	m := &memSource{}
	m.set(doc)
	return m
}

// startWithSources replaces the process-global sources with srcs for the
// duration of the test and starts an app that polls them quickly.
func startWithSources(t *testing.T, srcs ...Source) *bosun.App {
	t.Helper()
	old := pendingSources
	pendingSources = nil
	for _, s := range srcs {
		Add(s)
	}
	t.Cleanup(func() { pendingSources = old })

	app := bosun.New()
	registry.RegisterInstance[*Options](app.Reg, &Options{Poll: 10 * time.Millisecond})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

func dynamicOf[T any](t *testing.T, app *bosun.App) *bosun.Dynamic[T] {
	t.Helper()
	v, err := app.Reg.ResolveType(reflect.TypeOf((**bosun.Dynamic[T])(nil)).Elem())
	if err != nil {
		t.Fatal(err)
	}
	return v.(*bosun.Dynamic[T])
}

// --- merging ---

func TestMergeJSON(t *testing.T) {
	cases := []struct{ base, over, want string }{
		{`{"A":1,"B":2}`, `{"B":3}`, `{"A":1,"B":3}`},
		{`{"A":{"X":1,"Y":2}}`, `{"A":{"Y":3}}`, `{"A":{"X":1,"Y":3}}`},
		{`{"PerMinute":1}`, `{"PERMINUTE":2}`, `{"PerMinute":2}`}, // case-insensitive
		{`{"A":{"X":1}}`, `{"A":5}`, `{"A":5}`},                   // non-object replaces
		{`{"A":1}`, `[1,2]`, `[1,2]`},
	}
	for _, c := range cases {
		if got := string(mergeJSON(json.RawMessage(c.base), json.RawMessage(c.over))); got != c.want {
			t.Errorf("merge(%s, %s) = %s, want %s", c.base, c.over, got, c.want)
		}
	}
}

type mergeOpts struct {
	PerMinute int
	Burst     int
}

var _ = Bind[mergeOpts]("mergetest")
var _ = bosun.DefaultDynamic[mergeOpts](func() *mergeOpts { return &mergeOpts{} })

func TestWatcherMergesFieldsAcrossSources(t *testing.T) {
	file := newMemSource(map[string]string{"mergetest": `{"PerMinute":100,"Burst":5}`})
	env := EnvSource{Environ: func() []string {
		return []string{"MERGETEST_FOO=1", "MERGETEST_BURST=7"}
	}}
	app := startWithSources(t, file, env)

	got := dynamicOf[mergeOpts](t, app).Get()
	if got.PerMinute != 100 || got.Burst != 7 {
		t.Fatalf("file field must survive env override of another field: %+v", got)
	}
}

// --- per-key failures, defaults ---

type badOpts struct{ N int }
type goodOpts struct{ N int }

// bad is bound before good so a failure on bad would block good if apply
// stopped at the first error.
var _ = Bind[badOpts]("wbad")
var _ = Bind[goodOpts]("wgood")
var _ = bosun.DefaultDynamic[badOpts](func() *badOpts { return &badOpts{N: -1} })
var _ = bosun.DefaultDynamic[goodOpts](func() *goodOpts { return &goodOpts{N: -1} })

func TestWatcherFailingKeyDoesNotBlockOthers(t *testing.T) {
	src := newMemSource(map[string]string{"wbad": `{"N":1}`, "wgood": `{"N":1}`})
	app := startWithSources(t, src)
	bad, good := dynamicOf[badOpts](t, app), dynamicOf[goodOpts](t, app)

	src.set(map[string]string{"wbad": `{"N":"not a number"}`, "wgood": `{"N":2}`})
	waitFor(t, func() bool { return good.Get().N == 2 }, "good key should update despite bad key")
	if bad.Get().N != 1 {
		t.Fatalf("bad key should keep its previous value, got %d", bad.Get().N)
	}

	// once fixed, the bad key applies again
	src.set(map[string]string{"wbad": `{"N":3}`, "wgood": `{"N":2}`})
	waitFor(t, func() bool { return bad.Get().N == 3 }, "fixed key should apply")
}

func TestWatcherStartupReportsEveryFailingKey(t *testing.T) {
	old := pendingSources
	pendingSources = nil
	Add(newMemSource(map[string]string{"wbad": `{"N":"x"}`, "mergetest": `{"Burst":"y"}`}))
	defer func() { pendingSources = old }()

	err := bosun.New().Start()
	if err == nil || !strings.Contains(err.Error(), `"wbad"`) || !strings.Contains(err.Error(), `"mergetest"`) {
		t.Fatalf("startup should fail naming both keys, got %v", err)
	}
}

func TestWatcherRestoresDefaultWhenKeyRemoved(t *testing.T) {
	src := newMemSource(map[string]string{"wgood": `{"N":5}`})
	app := startWithSources(t, src)
	good := dynamicOf[goodOpts](t, app)
	if good.Get().N != 5 {
		t.Fatalf("initial load: got %d", good.Get().N)
	}

	src.set(nil)
	waitFor(t, func() bool { return good.Get().N == -1 }, "removed key should fall back to the default")

	src.set(map[string]string{"wgood": `{"N":6}`})
	waitFor(t, func() bool { return good.Get().N == 6 }, "key should apply again after coming back")
}

// --- shutdown ---

func TestWatcherStopsOnShutdown(t *testing.T) {
	app := startWithSources(t, newMemSource(map[string]string{"wgood": `{"N":1}`}))
	v, err := app.Reg.ResolveType(reflect.TypeOf((*Watcher)(nil)))
	if err != nil {
		t.Fatal(err)
	}
	w := v.(*Watcher)

	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown() }()
	select {
	case err := <-shut:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app.Shutdown did not return")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("poll goroutine still running after Shutdown")
	}
	if err := w.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
}
