// Package webhookmod receives inbound webhooks. It mounts a route per provider,
// verifies the signature, and dispatches to handlers registered for a provider
// and event type. Verifiers for GitHub, Stripe, and a generic HMAC scheme ship
// in the box; register your own by adding to Options.Verifiers. A provider with
// no verifier is rejected with 401; use Unverified() to accept a provider's
// events without verification during local development.
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
	"strconv"
	"strings"
	"time"

	"github.com/bluebeard63/bosun"
)

// DefaultMaxBody is the request body limit used when Options.MaxBody is zero.
const DefaultMaxBody = 1 << 20 // 1 MiB

// DefaultStripeTolerance is the maximum age of a Stripe-Signature timestamp
// used when StripeVerifier.Tolerance is zero, matching Stripe's own libraries.
const DefaultStripeTolerance = 5 * time.Minute

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
	// verifier is rejected with 401; map it to Unverified() to accept its
	// events without verification (local development only).
	Verifiers map[string]Verifier
	// MaxBody is the largest accepted request body in bytes; a larger body is
	// rejected with 413. Zero means DefaultMaxBody.
	MaxBody int64
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
	limit := c.Opts.MaxBody
	if limit <= 0 {
		limit = DefaultMaxBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	v, ok := c.Opts.Verifiers[provider]
	if !ok || v == nil {
		http.Error(w, "no verifier configured", http.StatusUnauthorized)
		return
	}
	if err := v.Verify(r.Header, body); err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
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

// Unverified returns a verifier that accepts every request without checking a
// signature. It is the explicit opt-out for local development and for a
// provider that is already authenticated by a trusted proxy; never use it for
// a public endpoint.
func Unverified() Verifier { return unverified{} }

type unverified struct{}

func (unverified) Verify(http.Header, []byte) error { return nil }

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
// The request is accepted if any v1 signature matches, so verification keeps
// working while a secret is being rolled.
type StripeVerifier struct {
	Secret string
	// Tolerance is the maximum age (or clock skew) of the signed timestamp;
	// older events are rejected to prevent replay. Zero means
	// DefaultStripeTolerance.
	Tolerance time.Duration
}

func (v StripeVerifier) Verify(h http.Header, body []byte) error {
	sig := h.Get("Stripe-Signature")
	var ts string
	var v1s []string
	for _, part := range strings.Split(sig, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = val
		case "v1":
			v1s = append(v1s, val)
		}
	}
	if ts == "" || len(v1s) == 0 {
		return errors.New("webhookmod: malformed Stripe-Signature")
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("webhookmod: malformed Stripe-Signature timestamp")
	}
	tolerance := v.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultStripeTolerance
	}
	if age := time.Since(time.Unix(secs, 0)); age > tolerance || age < -tolerance {
		return errors.New("webhookmod: Stripe-Signature timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(v.Secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, v1 := range v1s {
		if hmac.Equal([]byte(want), []byte(v1)) {
			return nil
		}
	}
	return errors.New("webhookmod: signature mismatch")
}
