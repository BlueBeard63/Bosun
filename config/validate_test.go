package config

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
)

// startErr starts an app over srcs and returns app.Start's error.
func startErr(t *testing.T, srcs ...Source) error {
	t.Helper()
	old := pendingSources
	pendingSources = nil
	for _, s := range srcs {
		Add(s)
	}
	defer func() { pendingSources = old }()
	app := bosun.New()
	err := app.Start()
	if err == nil {
		_ = app.Shutdown()
	}
	return err
}

// --- unknown fields ---

type vInner struct {
	Max int `json:"max"`
}

type vEmbedded struct {
	Region string
}

type vOpts struct {
	vEmbedded
	PerMinute int `json:"per_minute"`
	Inner     vInner
	Hidden    string `json:"-"`
	Labels    map[string]vInner
	List      []vInner
	Any       map[string]any
	Raw       json.RawMessage
}

func (o vOpts) Validate() error { // value receiver
	if o.PerMinute < 0 {
		return errors.New("per_minute must not be negative")
	}
	return nil
}

var _ = Bind[vOpts]("vtest")
var _ = bosun.DefaultDynamic[vOpts](func() *vOpts { return &vOpts{PerMinute: 60} })

func TestUnknownFieldsPaths(t *testing.T) {
	doc := `{
		"PER_MINUTE": 1, "per_minute": 2, "region": "eu",
		"Hidden": "x",
		"Inner": {"max": 1, "MAX": 2, "min": 3},
		"Labels": {"a": {"max": 1, "typo": 2}},
		"List": [{"max": 1}, {"nope": 1}],
		"Any": {"whatever": {"deep": 1}},
		"Raw": {"free": "form"},
		"PerMinite": 5
	}`
	var got []string
	unknownFields(reflect.TypeOf(vOpts{}), json.RawMessage(doc), "", &got)
	want := []string{"Hidden", "Inner.min", "Labels.a.typo", "List[1].nope", "PerMinite"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unknown fields = %v, want %v", got, want)
	}
}

func TestDecodeStrict(t *testing.T) {
	var v vOpts
	if err := decodeStrict(json.RawMessage(`{"PER_MINUTE":3}`), &v); err != nil || v.PerMinute != 3 {
		t.Fatalf("case-insensitive field should decode: %v %+v", err, v)
	}
	if err := decodeStrict(json.RawMessage(`{"PerMinite":3}`), &v); err == nil || !strings.Contains(err.Error(), "PerMinite") {
		t.Fatalf("unknown field should be rejected, got %v", err)
	}
	if err := decodeStrict(json.RawMessage(`{} {}`), &v); err == nil {
		t.Fatal("trailing data should be rejected")
	}
}

func TestUnknownFieldInFileSourceIsFatalAtStartup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(`{"vtest":{"Inner":{"max":1,"Mx":2},"PerMinite":5}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := startErr(t, FileSource{Path: p})
	if err == nil {
		t.Fatal("unknown field should fail startup")
	}
	for _, want := range []string{`config key "vtest"`, `"Inner.Mx"`, `"PerMinite"`, "file " + p} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

func TestUnknownFieldNamesTheRightSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(`{"vtest":{"per_minute":5}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	kv := &memKV{rows: map[string][]byte{"vtest": []byte(`{"PerMinite":7}`)}}
	err := startErr(t, FileSource{Path: p}, KVSource{Store: kv})
	if err == nil || !strings.Contains(err.Error(), "in kv store") || strings.Contains(err.Error(), "file ") {
		t.Fatalf("error should blame only the kv store, got %v", err)
	}
}

func TestCaseInsensitiveMergeIsNotUnknown(t *testing.T) {
	// the file and env spell the field differently; both are known
	file := newMemSource(map[string]string{"vtest": `{"PER_MINUTE":5,"inner":{"MAX":1}}`})
	env := EnvSource{Environ: func() []string { return []string{"VTEST_PER_MINUTE=7", "VTEST_INNER__MAX=2"} }}
	app := startWithSources(t, file, env)
	got := dynamicOf[vOpts](t, app).Get()
	if got.PerMinute != 7 || got.Inner.Max != 2 {
		t.Fatalf("env should override case-insensitively: %+v", got)
	}
}

func TestUnknownFieldAtRuntimeKeepsPreviousValue(t *testing.T) {
	src := newMemSource(map[string]string{"vtest": `{"per_minute":5}`, "wgood": `{"N":1}`})
	app := startWithSources(t, src)
	v, good := dynamicOf[vOpts](t, app), dynamicOf[goodOpts](t, app)

	src.set(map[string]string{"vtest": `{"per_minute":9,"PerMinite":9}`, "wgood": `{"N":2}`})
	waitFor(t, func() bool { return good.Get().N == 2 }, "other keys should still update")
	if v.Get().PerMinute != 5 {
		t.Fatalf("key with an unknown field should keep its previous value, got %d", v.Get().PerMinute)
	}
}

// --- Validate hook ---

type vPtrOpts struct{ Name string }

func (o *vPtrOpts) Validate() error { // pointer receiver
	if o.Name == "" {
		return errors.New("name is required")
	}
	return nil
}

var _ = Bind[vPtrOpts]("vptr")
var _ = bosun.DefaultDynamic[vPtrOpts](func() *vPtrOpts { return &vPtrOpts{Name: "default"} })

func TestValidateErrorIsFatalAtStartup(t *testing.T) {
	err := startErr(t, newMemSource(map[string]string{
		"vtest": `{"per_minute":-1}`,
		"vptr":  `{"Name":""}`,
	}))
	if err == nil {
		t.Fatal("Validate error should fail startup")
	}
	for _, want := range []string{`config key "vtest": invalid: per_minute must not be negative`, `config key "vptr": invalid: name is required`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

func TestValidateErrorAtRuntimeKeepsPreviousValue(t *testing.T) {
	src := newMemSource(map[string]string{"vtest": `{"per_minute":5}`, "vptr": `{"Name":"a"}`, "wgood": `{"N":1}`})
	app := startWithSources(t, src)
	v, p, good := dynamicOf[vOpts](t, app), dynamicOf[vPtrOpts](t, app), dynamicOf[goodOpts](t, app)

	src.set(map[string]string{"vtest": `{"per_minute":-3}`, "vptr": `{"Name":""}`, "wgood": `{"N":2}`})
	waitFor(t, func() bool { return good.Get().N == 2 }, "other keys should still update")
	if v.Get().PerMinute != 5 || p.Get().Name != "a" {
		t.Fatalf("invalid values should keep the previous ones, got %+v %+v", v.Get(), p.Get())
	}

	src.set(map[string]string{"vtest": `{"per_minute":8}`, "vptr": `{"Name":"b"}`, "wgood": `{"N":2}`})
	waitFor(t, func() bool { return v.Get().PerMinute == 8 && p.Get().Name == "b" }, "valid values should apply again")
}

// --- SecretBox key binding ---

func newTestBox(t *testing.T) *SecretBox {
	t.Helper()
	key := sha256.Sum256([]byte("k"))
	box, err := NewSecretBox(key[:])
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSecretBoxCiphertextMovedToAnotherKeyFails(t *testing.T) {
	box := newTestBox(t)
	sealed, err := box.Seal("ratelimit", []byte(`{"N":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open("ratelimit", sealed); err != nil {
		t.Fatalf("same key should open: %v", err)
	}
	if _, err := box.Open("featureflags", sealed); err == nil {
		t.Fatal("ciphertext moved to another key must not open")
	}

	// the same through KVSource: a row copied under another key
	kv := &memKV{rows: map[string][]byte{"featureflags": sealed}}
	_, err = KVSource{Store: kv, Decrypt: box.Open}.Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), `"featureflags"`) {
		t.Fatalf("moved row should fail naming its key, got %v", err)
	}
}

// sealLegacy produces the pre-key-binding format: nonce || ciphertext.
func sealLegacy(t *testing.T, box *SecretBox, nonce0 byte, plain []byte) []byte {
	t.Helper()
	nonce := make([]byte, box.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	nonce[0] = nonce0
	return box.aead.Seal(nonce, nonce, plain, nil)
}

func TestSecretBoxLegacyMigration(t *testing.T) {
	box := newTestBox(t)
	plain := []byte(`{"N":2}`)
	// nonce starting 0x01 looks like the current version prefix
	for _, nonce0 := range []byte{0x00, sealV1, 0xff} {
		legacy := sealLegacy(t, box, nonce0, plain)

		if _, err := box.Open("k", legacy); err == nil {
			t.Fatalf("nonce0=%#x: legacy value must be rejected by default", nonce0)
		}

		lenient := newTestBox(t)
		lenient.AllowLegacy = true
		if got, err := lenient.Open("k", legacy); err != nil || string(got) != string(plain) {
			t.Fatalf("nonce0=%#x: AllowLegacy should open legacy values: %v %s", nonce0, err, got)
		}

		resealed, err := box.Reseal("k", legacy)
		if err != nil {
			t.Fatalf("nonce0=%#x: reseal: %v", nonce0, err)
		}
		if got, err := box.Open("k", resealed); err != nil || string(got) != string(plain) {
			t.Fatalf("nonce0=%#x: resealed value should open: %v %s", nonce0, err, got)
		}
		if _, err := lenient.Open("other", resealed); err == nil {
			t.Fatalf("nonce0=%#x: resealed value must be bound to its key", nonce0)
		}
		again, err := box.Reseal("k", resealed)
		if err != nil || string(again) != string(resealed) {
			t.Fatalf("nonce0=%#x: resealing a current value should be a no-op: %v", nonce0, err)
		}
	}
}
