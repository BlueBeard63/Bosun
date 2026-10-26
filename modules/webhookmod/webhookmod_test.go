package webhookmod_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/webhookmod"
	"github.com/bluebeard63/bosun/registry"
)

func ghSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var received struct {
	sync.Mutex
	events []webhookmod.Event
}

func init() {
	webhookmod.Handle("github", "push", func(_ context.Context, e webhookmod.Event) error {
		received.Lock()
		received.events = append(received.events, e)
		received.Unlock()
		return nil
	})
}

func newApp(t *testing.T, secret string) *bosun.App {
	t.Helper()
	app := bosun.New()
	registry.RegisterInstance[*webhookmod.Options](app.Reg, &webhookmod.Options{
		Path: "/webhooks",
		Verifiers: map[string]webhookmod.Verifier{
			"github": webhookmod.GitHubVerifier(secret),
		},
	})
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app
}

func newAppWith(t *testing.T, opts *webhookmod.Options) *bosun.App {
	t.Helper()
	app := bosun.New()
	registry.RegisterInstance[*webhookmod.Options](app.Reg, opts)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app
}

func post(app *bosun.App, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	return postTo(app, "/webhooks/github", body, headers)
}

func postTo(app *bosun.App, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	return rec
}

func TestValidSignatureDispatches(t *testing.T) {
	received.Lock()
	received.events = nil
	received.Unlock()

	app := newApp(t, "s3cr3t")
	body := []byte(`{"ref":"refs/heads/main"}`)
	rec := post(app, body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": ghSign("s3cr3t", body),
		"X-GitHub-Delivery":   "abc-123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	received.Lock()
	defer received.Unlock()
	if len(received.events) != 1 {
		t.Fatalf("handler received %d events, want 1", len(received.events))
	}
	if received.events[0].ID != "abc-123" || received.events[0].Type != "push" {
		t.Fatalf("event = %+v", received.events[0])
	}
}

func TestInvalidSignatureRejected(t *testing.T) {
	app := newApp(t, "s3cr3t")
	body := []byte(`{"ref":"main"}`)
	rec := post(app, body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": ghSign("wrong-secret", body),
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestUnregisteredEventTypeAccepted(t *testing.T) {
	received.Lock()
	received.events = nil
	received.Unlock()

	app := newApp(t, "s3cr3t")
	body := []byte(`{}`)
	rec := post(app, body, map[string]string{
		"X-GitHub-Event":      "issues", // no handler for this type
		"X-Hub-Signature-256": ghSign("s3cr3t", body),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	received.Lock()
	defer received.Unlock()
	if len(received.events) != 0 {
		t.Fatalf("push handler should not fire for issues event")
	}
}

func TestUnconfiguredProviderRejected(t *testing.T) {
	app := newAppWith(t, &webhookmod.Options{Path: "/webhooks"})
	rec := post(app, []byte(`{}`), map[string]string{"X-GitHub-Event": "push"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestUnverifiedOptOutAccepted(t *testing.T) {
	app := newAppWith(t, &webhookmod.Options{
		Path:      "/webhooks",
		Verifiers: map[string]webhookmod.Verifier{"local": webhookmod.Unverified()},
	})
	rec := postTo(app, "/webhooks/local", []byte(`{}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
}

func TestOversizedBodyReturns413(t *testing.T) {
	app := newAppWith(t, &webhookmod.Options{
		Path:      "/webhooks",
		MaxBody:   16,
		Verifiers: map[string]webhookmod.Verifier{"github": webhookmod.GitHubVerifier("s3cr3t")},
	})
	body := bytes.Repeat([]byte("x"), 17)
	rec := post(app, body, map[string]string{
		"X-GitHub-Event":      "push",
		"X-Hub-Signature-256": ghSign("s3cr3t", body),
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func stripeSign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func stripeHeader(ts int64, sigs ...string) http.Header {
	parts := []string{"t=" + strconv.FormatInt(ts, 10)}
	for _, s := range sigs {
		parts = append(parts, "v1="+s)
	}
	h := http.Header{}
	h.Set("Stripe-Signature", strings.Join(parts, ","))
	return h
}

func TestStripeValidSignature(t *testing.T) {
	body := []byte(`{"type":"charge.succeeded"}`)
	ts := time.Now().Unix()
	v := webhookmod.StripeVerifier{Secret: "whsec"}
	if err := v.Verify(stripeHeader(ts, stripeSign("whsec", ts, body)), body); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestStripeOldTimestampRejected(t *testing.T) {
	body := []byte(`{"type":"charge.succeeded"}`)
	ts := time.Now().Add(-10 * time.Minute).Unix()
	h := stripeHeader(ts, stripeSign("whsec", ts, body))
	if err := (webhookmod.StripeVerifier{Secret: "whsec"}).Verify(h, body); err == nil {
		t.Fatal("event older than the default tolerance was accepted")
	}
	// A wider tolerance accepts the same event.
	if err := (webhookmod.StripeVerifier{Secret: "whsec", Tolerance: time.Hour}).Verify(h, body); err != nil {
		t.Fatalf("Verify with 1h tolerance: %v", err)
	}
}

func TestStripeAnyV1SignatureMatches(t *testing.T) {
	body := []byte(`{"type":"charge.succeeded"}`)
	ts := time.Now().Unix()
	h := stripeHeader(ts, stripeSign("old-secret", ts, body), stripeSign("whsec", ts, body))
	if err := (webhookmod.StripeVerifier{Secret: "whsec"}).Verify(h, body); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	h = stripeHeader(ts, stripeSign("old-secret", ts, body), stripeSign("other", ts, body))
	if err := (webhookmod.StripeVerifier{Secret: "whsec"}).Verify(h, body); err == nil {
		t.Fatal("no matching v1 signature, but Verify succeeded")
	}
}
