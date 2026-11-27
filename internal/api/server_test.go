package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"baremetal-rollout/internal/fleet"
	"baremetal-rollout/internal/report"
)

func TestHTTPAPIAndControls(t *testing.T) {
	base := fleet.Generate(120, 42)
	comp, err := report.RunComparison(base, 42)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(base, comp, 42)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/api/state", "/api/racks", "/api/hardware-checks", "/api/comparison", "/api/plan"} {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}

	root := get("/")
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "裸金属") {
		t.Fatalf("embedded index did not render: status=%d bytes=%d", root.Code, root.Body.Len())
	}

	post("/api/live/finish")
	stateRec := get("/api/state")
	var state StateResponse
	if err := json.Unmarshal(stateRec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Summary.Done || state.Summary.Total != 120 {
		t.Fatalf("unexpected finished state: done=%v total=%d", state.Summary.Done, state.Summary.Total)
	}
	if len(state.Machines) != 120 {
		t.Fatalf("machine views=%d", len(state.Machines))
	}

	id := state.Machines[0].ID
	detail := get("/api/machines/" + id)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"logs"`) {
		t.Fatalf("machine detail failed: code=%d body=%s", detail.Code, detail.Body.String()[:min(200, detail.Body.Len())])
	}

	post("/api/live/reset?mode=storm")
	post("/api/live/speed?speed=fast")
	post("/api/live/pause")
	post("/api/live/resume")
}
