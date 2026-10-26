package bosun_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/modules/repomod"
	"github.com/bluebeard63/bosun/modules/storemod"
)

// The module sentinels map to 404 out of the box: they carry their own
// HTTPStatus, so the root package needs no import of the modules.

type sentinelCtrl struct{}

var _ = bosun.Controller[sentinelCtrl]("/sentinels")

func (c *sentinelCtrl) Routes(r *bosun.Router) {
	bosun.Get(r, "/{kind}", c.get)
}

func (c *sentinelCtrl) get(_ context.Context, req *bosun.Req[struct{}]) (struct{}, error) {
	if req.Params["kind"] == "store" {
		return struct{}{}, fmt.Errorf("download report: %w", storemod.ErrNotFound)
	}
	return struct{}{}, fmt.Errorf("load user 7: %w", repomod.ErrNotFound)
}

func TestModuleSentinelsAre404(t *testing.T) {
	app := bosun.New()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"store", "repo"} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/sentinels/"+kind, nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error":"not found"`) {
			t.Errorf("%s: got %d %s, want 404 not found", kind, rec.Code, rec.Body)
		}
	}
}

func TestModuleSentinelOverride(t *testing.T) {
	app := bosun.New(bosun.MapError(storemod.ErrNotFound, http.StatusGone, "expired"))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest("GET", "/sentinels/store", nil))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"error":"expired"`) {
		t.Fatalf("got %d %s, want 410 expired", rec.Code, rec.Body)
	}
}
