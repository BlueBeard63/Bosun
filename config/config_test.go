package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

// --- FileSource ---

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	if err := os.WriteFile(p, []byte(`{"k":{"N":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := FileSource{Path: p}.Load(context.Background())
	if err != nil || string(doc["k"]) != `{"N":1}` {
		t.Fatalf("load: %v %v", err, doc)
	}
	// missing file is silently optional
	doc, err = FileSource{Path: filepath.Join(dir, "missing.json")}.Load(context.Background())
	if err != nil || doc != nil {
		t.Fatalf("missing file should yield nil, nil: %v %v", err, doc)
	}
	// malformed file is an error
	_ = os.WriteFile(p, []byte(`{broken`), 0o644)
	if _, err = (FileSource{Path: p}).Load(context.Background()); err == nil {
		t.Fatal("malformed JSON should error")
	}
}

// --- EnvSource ---

type envOpts struct {
	N    int
	On   bool
	Name string
	R    float64
}

var _ = Bind[envOpts]("envtest")
var _ = bosun.DefaultDynamic[envOpts](func() *envOpts { return &envOpts{} })

func TestEnvSource(t *testing.T) {
	src := EnvSource{Environ: func() []string {
		return []string{
			"ENVTEST_N=5",
			"ENVTEST_ON=true",
			"ENVTEST_NAME=jack",
			"ENVTEST_R=1.5",
			"UNRELATED=x",
		}
	}}
	doc, err := src.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got envOpts
	if err := json.Unmarshal(doc["envtest"], &got); err != nil {
		t.Fatal(err)
	}
	if got.N != 5 || !got.On || got.Name != "jack" || got.R != 1.5 {
		t.Fatalf("env typing wrong: %+v", got)
	}
	if _, ok := doc["unrelated"]; ok {
		t.Fatal("unbound prefixes must not appear")
	}
}

// --- SecretBox + KVSource ---

func TestSecretBoxRoundTripAndTamper(t *testing.T) {
	key := sha256.Sum256([]byte("k"))
	box, err := NewSecretBox(key[:])
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(`{"N":7}`))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(sealed)
	if err != nil || string(plain) != `{"N":7}` {
		t.Fatalf("roundtrip failed: %v %s", err, plain)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext must not open")
	}
	if _, err := box.Open([]byte("short")); err == nil {
		t.Fatal("short input must error")
	}
	if _, err := NewSecretBox([]byte("badlen")); err == nil {
		t.Fatal("bad key length must error")
	}
}

type memKV struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *memKV) All(ctx context.Context) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range m.rows {
		out[k] = v
	}
	return out, nil
}

func (m *memKV) put(k string, v []byte) {
	m.mu.Lock()
	m.rows[k] = v
	m.mu.Unlock()
}

func TestKVSourceWithDecryption(t *testing.T) {
	key := sha256.Sum256([]byte("k"))
	box, _ := NewSecretBox(key[:])
	sealed, _ := box.Seal([]byte(`{"N":3}`))
	kv := &memKV{rows: map[string][]byte{"envtest": sealed}}

	doc, err := KVSource{Store: kv, Decrypt: box.Open}.Load(context.Background())
	if err != nil || string(doc["envtest"]) != `{"N":3}` {
		t.Fatalf("kv decrypt load failed: %v %v", err, doc)
	}
	// corrupted row surfaces as error
	kv.put("envtest", []byte("garbage"))
	if _, err := (KVSource{Store: kv, Decrypt: box.Open}).Load(context.Background()); err == nil {
		t.Fatal("undecryptable value must error")
	}
	// without Decrypt values pass through
	kv.put("envtest", []byte(`{"N":9}`))
	doc, err = KVSource{Store: kv}.Load(context.Background())
	if err != nil || string(doc["envtest"]) != `{"N":9}` {
		t.Fatal("plaintext KV passthrough failed")
	}
}

// --- watcher integration: layering + hot reload ---
// Sources registered via Add are process-global, so all layering is
// exercised in this single integration test.

type wOpts struct{ N int }

var _ = Bind[wOpts]("w")
var _ = bosun.DefaultDynamic[wOpts](func() *wOpts { return &wOpts{N: -1} })

func TestWatcherLayeringAndHotReload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	if err := os.WriteFile(p, []byte(`{"w":{"N":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("k"))
	box, _ := NewSecretBox(key[:])
	kv := &memKV{rows: map[string][]byte{}}

	Add(FileSource{Path: p})
	Add(EnvSource{Environ: func() []string { return []string{"W_N=2"} }})
	Add(KVSource{Store: kv, Decrypt: box.Open})

	app := bosun.New()
	registry.RegisterInstance[*Options](app.Reg, &Options{Poll: 25 * time.Millisecond})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}

	dynT := reflect.TypeOf((**bosun.Dynamic[wOpts])(nil)).Elem()
	v, err := app.Reg.ResolveType(dynT)
	if err != nil {
		t.Fatal(err)
	}
	dyn := v.(*bosun.Dynamic[wOpts])

	if dyn.Get().N != 2 {
		t.Fatalf("env should beat file: got %d", dyn.Get().N)
	}

	sealed, _ := box.Seal([]byte(`{"N":3}`))
	kv.put("w", sealed)
	waitFor(t, func() bool { return dyn.Get().N == 3 }, "encrypted KV should beat env")

	sealed, _ = box.Seal([]byte(`{"N":4}`))
	kv.put("w", sealed)
	waitFor(t, func() bool { return dyn.Get().N == 4 }, "live KV update should apply")
}

func TestWatcherStartupFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(p, []byte(`{nope`), 0o644)
	Add(FileSource{Path: p})
	// remove the broken source afterwards so other apps in this binary work
	defer func() { pendingSources = pendingSources[:len(pendingSources)-1] }()

	app := bosun.New()
	err := app.Start()
	if err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Fatalf("expected startup parse failure, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// --- AddSource: registry-resolved sources ---

type diSource struct{}

func (s *diSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	return nil, nil
}

var _ = bosun.Service[diSource]()

func TestAddSourceFromRegistry(t *testing.T) {
	AddSource[diSource]()
	defer func() { pendingSources = pendingSources[:len(pendingSources)-1] }()
	app := bosun.New()
	registry.RegisterInstance[*Options](app.Reg, &Options{Poll: time.Hour})
	if err := app.Start(); err != nil {
		t.Fatalf("registry-resolved source should build: %v", err)
	}
}
