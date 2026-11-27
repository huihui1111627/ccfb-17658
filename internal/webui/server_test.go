package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerEndpoints(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	cases := []struct {
		path string
		code int
	}{
		{"/api/meta", 200},
		{"/api/overview", 200},
		{"/api/audit", 200},
		{"/api/compare", 200},
		{"/api/run/naive/wall", 200},
		{"/api/run/rhythm/summary", 200},
		{"/api/run/rhythm/services", 200},
		{"/api/run/rhythm/logs", 200},
		{"/api/run/rhythm/machines", 200},
		{"/api/machine/rhythm/2", 200},
		{"/api/cabinet/rhythm/3", 200},
		{"/api/run/bogus/wall", 404},
		{"/api/machine/rhythm/99999", 404},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", c.path, nil))
		if rr.Code != c.code {
			t.Errorf("%s: got %d want %d", c.path, rr.Code, c.code)
		}
	}
}

func TestWallPayloadComplete(t *testing.T) {
	s, _ := New()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/run/rhythm/wall", nil))
	var payload struct {
		Hosts []struct {
			ID      int    `json:"id"`
			Phase   string `json:"phase"`
			Verdict string `json:"verdict"`
		} `json:"hosts"`
		Transitions map[int][]json.RawMessage `json:"transitions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Hosts) != 800 {
		t.Fatalf("expected 800 hosts, got %d", len(payload.Hosts))
	}
}

func TestStaticAssets(t *testing.T) {
	s, _ := New()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: %d", path, rr.Code)
		}
		if rr.Body.Len() == 0 {
			t.Errorf("%s: empty", path)
		}
	}
}
