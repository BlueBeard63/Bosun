package webhookmod_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/modules/webhookmod"
	"github.com/amberstack/bosun/registry"
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

func post(app *bosun.App, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(body))
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
		"X-GitHub-Event":       "push",
		"X-Hub-Signature-256":  ghSign("s3cr3t", body),
		"X-GitHub-Delivery":    "abc-123",
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
