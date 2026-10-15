package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadiness(t *testing.T) {
	c := NewChecker()
	c.Add("db", func(context.Context) error { return nil })

	rec := httptest.NewRecorder()
	c.ReadinessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	c.Add("dhcp", func(context.Context) error { return errors.New("not listening") })
	rec = httptest.NewRecorder()
	c.ReadinessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rec.Code)
	}
	var res Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Checks["dhcp"] != "not listening" || res.Checks["db"] != "ok" || res.Status != "unavailable" {
		t.Fatalf("unexpected result %+v", res)
	}

	c.Remove("dhcp")
	if _, ok := c.Evaluate(context.Background()); !ok {
		t.Fatal("expected ready after removing failing check")
	}
}

func TestLiveness(t *testing.T) {
	rec := httptest.NewRecorder()
	LivenessHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}
