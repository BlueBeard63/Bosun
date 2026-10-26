package eventnatsmod

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/modules/eventmod"
)

// A requeue that can't be published must surface the error rather than
// report success.
func TestNackRequeueFailureReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &delivery{bus: &Bus{}, retry: retrySubject("s", ""), msg: eventmod.Message{Subject: "s", Attempt: 1}}
	if err := d.Nack(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("Nack error = %v, want context.Canceled", err)
	}
}

func TestRetrySubject(t *testing.T) {
	// Members of one group, in any process, must agree on the retry subject...
	if a, b := retrySubject("orders.>", "billing"), retrySubject("orders.>", "billing"); a != b {
		t.Fatalf("group retry subjects differ: %q vs %q", a, b)
	}
	// ...which must differ per group and per subject, and contain no wildcard.
	seen := map[string]bool{}
	for _, s := range []string{
		retrySubject("orders.>", "billing"),
		retrySubject("orders.>", "audit"),
		retrySubject("orders.placed", "billing"),
		retrySubject("orders.>", ""),
		retrySubject("orders.>", ""),
	} {
		if seen[s] {
			t.Fatalf("retry subject %q reused", s)
		}
		seen[s] = true
		if strings.ContainsAny(s, "*>") {
			t.Fatalf("retry subject %q contains a wildcard", s)
		}
	}
}
