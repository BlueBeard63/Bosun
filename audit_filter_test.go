package bosun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/bluebeard63/bosun/registry"
)

// countedText counts how often it is rendered as text: once by the JSON
// response encoder, and once more each time Redact walks it.
type countedText string

var countedRenders atomic.Int64

func (c countedText) MarshalText() ([]byte, error) {
	countedRenders.Add(1)
	return []byte(c), nil
}

type auditRow struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Token string `json:"token"`
}

type auditFilterCtrl struct{}

var _ = Controller[auditFilterCtrl]("/auditfilter")

func (c *auditFilterCtrl) Routes(r *Router) {
	Get(r, "/one", func(_ context.Context, req *Req[struct{}]) (struct{ V countedText }, error) {
		return struct{ V countedText }{V: "v"}, nil
	})
	Post(r, "/one", func(_ context.Context, req *Req[struct{}]) (struct{ V countedText }, error) {
		return struct{ V countedText }{V: "v"}, nil
	})
	Get(r, "/list", func(_ context.Context, req *Req[struct{}]) ([]auditRow, error) {
		return auditRows, nil
	})
}

var auditRows = func() []auditRow {
	rows := make([]auditRow, 500)
	for i := range rows {
		rows[i] = auditRow{ID: i, Name: "user " + strconv.Itoa(i), Email: "u@example.com", Token: "secret"}
	}
	return rows
}()

// filterAuditor records only non-GET events.
type filterAuditor struct {
	capAuditor
	asked atomic.Int64
}

func (a *filterAuditor) ShouldAudit(_ context.Context, ev AuditEvent) bool {
	a.asked.Add(1)
	if ev.Request != nil || ev.Response != nil {
		panic("ShouldAudit must run before redaction")
	}
	return ev.Method != http.MethodGet
}

func startWithAuditor(t testing.TB, aud Auditor) *App {
	t.Helper()
	app := New()
	if aud != nil {
		registry.RegisterInstance[Auditor](app.Reg, aud)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestAuditFilterSkipsRedaction(t *testing.T) {
	aud := &filterAuditor{}
	app := startWithAuditor(t, aud)

	countedRenders.Store(0)
	rec := do(app, "GET", "/auditfilter/one", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if n := countedRenders.Load(); n != 1 { // the response encoding only
		t.Fatalf("ignored event: rendered %d times, want 1 (Redact must not run)", n)
	}
	if aud.asked.Load() != 1 || len(aud.events) != 0 {
		t.Fatalf("asked %d, recorded %d; want 1, 0", aud.asked.Load(), len(aud.events))
	}

	countedRenders.Store(0)
	do(app, "POST", "/auditfilter/one", "")
	if n := countedRenders.Load(); n != 2 { // encoding + Redact of the response
		t.Fatalf("recorded event: rendered %d times, want 2", n)
	}
	ev := aud.last(t)
	if ev.Method != "POST" || ev.Status != 200 || ev.Response.(map[string]any)["V"] != "v" {
		t.Fatalf("recorded event wrong: %+v", ev)
	}
}

func TestAuditWithoutFilterRecordsEverything(t *testing.T) {
	aud := &capAuditor{}
	app := startWithAuditor(t, aud)
	do(app, "GET", "/auditfilter/list", "")
	ev := aud.last(t)
	rows := ev.Response.([]any)
	if len(rows) != len(auditRows) || rows[0].(map[string]any)["token"] != "[REDACTED]" {
		t.Fatalf("response not redacted as before: %v", rows[0])
	}
}

// BenchmarkAuditLargeList compares a 500-row list response with no auditor,
// an auditor that filters the event out, and one that records it. The
// filtered case should allocate about the same as no auditor.
func BenchmarkAuditLargeList(b *testing.B) {
	for _, bc := range []struct {
		name string
		aud  Auditor
	}{
		{"none", nil},
		{"filtered", &filterAuditor{}},
		{"recorded", discardAuditor{}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			app := startWithAuditor(b, bc.aud)
			req := httptest.NewRequest("GET", "/auditfilter/list", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				app.Mux.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}

type discardAuditor struct{}

func (discardAuditor) Audit(context.Context, AuditEvent) {}
