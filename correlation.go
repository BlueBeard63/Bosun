package bosun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Correlation-id propagation. A correlation id ties together every log line,
// audit event, and downstream call (HTTP or event-queue) belonging to one
// logical request. The mw.Correlation middleware sets it on the request
// context; the typed adapter copies it onto each AuditEvent; the event-queue
// carrier (eventmod) rides it across async boundaries.

const (
	// HeaderCorrelationID is the request/response header carrying the id.
	HeaderCorrelationID = "X-Correlation-ID"
	// HeaderTraceparent is the W3C Trace Context header; when present its
	// trace-id seeds the correlation id so HTTP and tracing agree.
	HeaderTraceparent = "traceparent"
)

type correlationCtxKey struct{}

// WithCorrelationID returns a copy of ctx carrying the correlation id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationCtxKey{}, id)
}

// CorrelationID returns the correlation id attached to ctx, or "" if none.
func CorrelationID(ctx context.Context) string {
	v, _ := ctx.Value(correlationCtxKey{}).(string)
	return v
}

type traceparentCtxKey struct{}

// WithTraceparent returns a copy of ctx carrying a raw W3C traceparent string.
// Tracing drivers use it to continue a remote trace across HTTP and the event
// bus without core Bosun depending on any tracing library.
func WithTraceparent(ctx context.Context, tp string) context.Context {
	return context.WithValue(ctx, traceparentCtxKey{}, tp)
}

// Traceparent returns the raw W3C traceparent attached to ctx, or "".
func Traceparent(ctx context.Context) string {
	v, _ := ctx.Value(traceparentCtxKey{}).(string)
	return v
}

// NewCorrelationID returns a fresh random 128-bit id as 32 hex chars.
func NewCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// CorrelationIDFromTraceparent extracts the 32-hex trace-id from a W3C
// traceparent header ("00-<traceid>-<spanid>-<flags>"), or "" if malformed.
func CorrelationIDFromTraceparent(tp string) string {
	parts := strings.Split(tp, "-")
	if len(parts) < 3 || len(parts[1]) != 32 {
		return ""
	}
	return parts[1]
}
