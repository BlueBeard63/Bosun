package audit

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/amberstack/bosun"
)

func TestSlogAuditor(t *testing.T) {
	var buf bytes.Buffer
	a := &SlogAuditor{log: slog.New(slog.NewTextHandler(&buf, nil))}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}

	a.Audit(context.Background(), bosun.AuditEvent{
		Method: "POST", Path: "/x", Route: "r", Status: 200,
		Duration: time.Millisecond, Request: map[string]any{"a": 1},
		Response: map[string]any{"b": 2},
	})
	if !strings.Contains(buf.String(), "level=INFO") || !strings.Contains(buf.String(), "status=200") {
		t.Fatalf("success event wrong: %s", buf.String())
	}

	buf.Reset()
	a.Audit(context.Background(), bosun.AuditEvent{
		Method: "POST", Path: "/x", Status: 500,
		Err: "boom", ErrOrigin: "f.go:1",
	})
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "error=boom") || !strings.Contains(out, "error_origin=f.go:1") {
		t.Fatalf("error event wrong: %s", out)
	}
}

func TestInitDefaultsLogger(t *testing.T) {
	a := &SlogAuditor{}
	if err := a.Init(); err != nil || a.log == nil {
		t.Fatal("Init should default the logger")
	}
}
