// Package webhookmod receives inbound webhooks. It mounts a route per provider,
// verifies the signature, and dispatches to handlers registered for a provider
// and event type. Verifiers for GitHub, Stripe, and a generic HMAC scheme ship
// in the box; register your own by adding to Options.Verifiers.
//
//	var _ = webhookmod.Handle("github", "push", func(ctx context.Context, e webhookmod.Event) error {
//	    return deploy(ctx, e.Payload)
//	})
package webhookmod

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bluebeard63/bosun"
)

const maxBody = 1 << 20 // 1 MiB

// Event is a received webhook.
type Event struct {
	Provider string
	Type     string
	ID       string
	Payload  []byte
	Headers  http.Header
}

// Handler processes a received webhook event.
type Handler func(ctx context.Context, e Event) error

// Verifier checks a request's signature against its body.
type Verifier interface {
	Verify(h http.Header, body []byte) error
}

// Options configures the receiver.
type Options struct {
	// Path is the mount prefix; the provider is appended, e.g. /webhooks/github.
	Path string
	// Verifiers maps a provider to its signature verifier. A provider with no
	// verifier is accepted without verification (use only behind a trusted proxy).
	Verifiers map[string]Verifier
}

var _ = bosun.Default[*Options](func() *Options { return &Options{Path: "/webhooks"} })

type registration struct {
	provider  string
	eventType string
	h         Handler
}

var pending []registration

// Handle registers a handler for a provider and event type. Use "*" as the
// event type to receive every event from a provider.
func Handle(provider, eventType string, h Handler) struct{} {
	pending = append(pending, registration{provider: provider, eventType: eventType, h: h})
	return struct{}{}
}

// WebhookController mounts the receiver route.
type WebhookController struct {
	Opts     *Options // injected
	handlers []registration
}

var _ = bosun.Controller[WebhookController]("")

// Init snapshots the registered handlers.
func (c *WebhookController) Init() error {
	c.handlers = append(c.handlers, pending...)
	return nil
}

func (c *WebhookController) Routes(r *bosun.Router) {
	r.Post(c.Opts.Path+"/{provider}", c.Receive)
}

// Receive verifies and dispatches an inbound webhook.
func (c *WebhookController) Receive(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if v, ok := c.Opts.Verifiers[provider]; ok {
		if err := v.Verify(r.Header, body); err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}
	e := Event{
		Provider: provider,
		Type:     typeFor(provider, r.Header, body),
		ID:       idFor(provider, r.Header),
		Payload:  body,
		Headers:  r.Header,
	}
	for _, reg := range c.handlers {
		if reg.provider != provider {
			continue
		}
		if reg.eventType != "*" && reg.eventType != e.Type {
			continue
		}
		if err := reg.h(r.Context(), e); err != nil {
			http.Error(w, "handler error", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func typeFor(provider string, h http.Header, body []byte) string {
	switch provider {
	case "github":
		return h.Get("X-GitHub-Event")
	case "stripe":
		var p struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(body, &p)
		return p.Type
	default:
		return h.Get("X-Event-Type")
	}
}

func idFor(provider string, h http.Header) string {
	if provider == "github" {
		return h.Get("X-GitHub-Delivery")
	}
	return h.Get("X-Event-Id")
}

// --- verifiers ---

// HMACVerifier checks a hex HMAC-SHA256 of the body against a header value,
// optionally with a prefix such as "sha256=".
type HMACVerifier struct {
	Secret string
	Header string
	Prefix string
}

func (v HMACVerifier) Verify(h http.Header, body []byte) error {
	mac := hmac.New(sha256.New, []byte(v.Secret))
	mac.Write(body)
	want := v.Prefix + hex.EncodeToString(mac.Sum(nil))
	got := h.Get(v.Header)
	if got == "" || !hmac.Equal([]byte(want), []byte(got)) {
		return errors.New("webhookmod: signature mismatch")
	}
	return nil
}

// GitHubVerifier verifies a GitHub X-Hub-Signature-256 header.
func GitHubVerifier(secret string) Verifier {
	return HMACVerifier{Secret: secret, Header: "X-Hub-Signature-256", Prefix: "sha256="}
}

// StripeVerifier verifies a Stripe-Signature header (the timestamp and v1 scheme).
type StripeVerifier struct {
	Secret string
}

func (v StripeVerifier) Verify(h http.Header, body []byte) error {
	sig := h.Get("Stripe-Signature")
	var ts, v1 string
	for _, part := range strings.Split(sig, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = val
		case "v1":
			v1 = val
		}
	}
	if ts == "" || v1 == "" {
		return errors.New("webhookmod: malformed Stripe-Signature")
	}
	mac := hmac.New(sha256.New, []byte(v.Secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(v1)) {
		return errors.New("webhookmod: signature mismatch")
	}
	return nil
}
