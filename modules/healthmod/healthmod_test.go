package healthmod

import (
	"net/http/httptest"
	"testing"

	"github.com/bluebeard63/bosun"
)

func TestHealthRoutes(t *testing.T) {
	app := bosun.New(bosun.OverridePrefix[HealthController]("/status"))
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/status/live", "/status/ready"} {
		rec := httptest.NewRecorder()
		app.Mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Fatalf("%s -> %d", p, rec.Code)
		}
	}
}
