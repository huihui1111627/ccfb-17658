// Package webui serves the provisioning dashboard: a JSON API over the
// computed runs plus the embedded single-page front end.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/model"
	"provisioning-sim/internal/report"
	"provisioning-sim/internal/run"
	"provisioning-sim/internal/sim"
	"provisioning-sim/internal/spec"
)

//go:embed web
var webFS embed.FS

// Server holds the computed batch and serves it.
type Server struct {
	all *run.All
	mux *http.ServeMux
}

// New computes everything and returns a server.
func New() (*Server, error) {
	t0 := time.Now()
	all := run.Compute()
	log.Printf("scenario computed: %d machines, 4 strategies in %s", len(all.Scenario.Machines), time.Since(t0))
	s := &Server{all: all, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	s.mux.HandleFunc("/api/meta", s.handleMeta)
	s.mux.HandleFunc("/api/overview", s.handleOverview)
	s.mux.HandleFunc("/api/audit", s.handleAudit)
	s.mux.HandleFunc("/api/compare", s.handleCompare)
	s.mux.HandleFunc("/api/run/", s.handleRunPath)     // /api/run/{kind}/wall etc.
	s.mux.HandleFunc("/api/machine/", s.handleMachine) // /api/machine/{kind}/{id}
	s.mux.HandleFunc("/api/cabinet/", s.handleCabinet)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Shared payloads
// ---------------------------------------------------------------------------

func (s *Server) meta() map[string]any {
	sc := s.all.Scenario
	an := s.all.Analysis
	return map[string]any{
		"total":       len(sc.Machines),
		"cabinets":    spec.CabinetCount,
		"perCabinet":  spec.PerCabinet,
		"models":      spec.ModelOrder,
		"seed":        sc.Seed,
		"pools":       sc.Pools,
		"templates":   sc.Templates,
		"procurement": sc.Procurement,
		"strategies":  s.strategyList(),
		"rhythm":      an.Rhythm,
		"bottlenecks": an.AllAtOnce,
		"probe":       an.Probe,
		"switches":    sc.Switches,
	}
}

func (s *Server) strategyList() []map[string]any {
	out := []map[string]any{}
	for _, k := range s.all.Order {
		b := s.all.Bundles[k]
		out = append(out, map[string]any{
			"kind": string(k), "label": b.Brief.Label, "note": b.Brief.Note,
			"waves": len(b.Waves), "preflight": b.Config.Preflight, "remediate": b.Config.Remediate,
		})
	}
	return out
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.meta())
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	sc := s.all.Scenario
	au := s.all.Audit
	overview := map[string]any{
		"total":      len(sc.Machines),
		"audit":      report.BuildAudit(au),
		"truth":      truthBreakdown(sc),
		"comparison": s.all.Compare(),
		"models":     fleetByModel(sc),
	}
	writeJSON(w, overview)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	sc := s.all.Scenario
	au := s.all.Audit
	rows := []map[string]any{}
	for _, m := range sc.Machines {
		res := au.Results[m.ID]
		row := map[string]any{
			"id": m.ID, "hostname": m.Hostname, "model": m.Model,
			"cabinet": m.Cabinet.ID, "rackU": m.RackU,
			"held": res.Hold, "code": res.Code, "repair": res.Repair,
			"issues": audit.IssuesFor(res),
		}
		rows = append(rows, row)
	}
	writeJSON(w, map[string]any{
		"summary":  report.BuildAudit(au),
		"rows":     rows,
		"switches": sc.Switches,
	})
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.all.Compare())
}

// handleRunPath dispatches /api/run/{kind}/{what}.
func (s *Server) handleRunPath(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/run/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	kind := run.Kind(parts[0])
	b := s.all.Bundles[kind]
	if b == nil {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "wall":
		writeJSON(w, s.wall(b))
	case "summary":
		writeJSON(w, b.Summary)
	case "machines":
		writeJSON(w, s.machineIndex(b))
	case "services":
		writeJSON(w, s.serviceData(b))
	case "logs":
		writeJSON(w, s.logStream(b))
	default:
		http.NotFound(w, r)
	}
}

// wall is the phase display: every host's current phase at the end, plus the
// reconstructed time-series for scrubbing.
func (s *Server) wall(b *run.Bundle) map[string]any {
	hosts := []map[string]any{}
	for _, m := range b.Result.Machines {
		hosts = append(hosts, map[string]any{
			"id": m.ID, "hostname": m.Hostname, "cabinet": m.Cabinet, "rackU": m.RackU,
			"model": m.Model, "phase": m.Phase, "phaseLabel": model.PhaseLabel(m.Phase),
			"fault": m.Fault, "faultLabel": labelOr(m.Fault),
			"auditCode": m.AuditCode, "auditLabel": labelOr(m.AuditCode),
			"verdict": m.Verdict, "truth": m.Truth, "truthLabel": model.RootCauseLabel(m.Truth),
			"hold": m.Held, "doneAt": m.DoneAtS, "wave": m.Wave,
		})
	}
	return map[string]any{
		"series":      b.Result.Series,
		"hosts":       hosts,
		"makespan":    b.Result.MakespanS,
		"waves":       b.Waves,
		"peak":        b.Result.Peak,
		"switches":    b.Result.Switches,
		"transitions": s.transitions(b),
	}
}

// transitions returns per-host phase-change records for scrub reconstruction.
// Only time and phase matter to the wall; detail strings would triple the
// payload for no rendering benefit.
func (s *Server) transitions(b *run.Bundle) map[int][][2]any {
	out := make(map[int][][2]any, len(b.Result.Trans))
	for id, tr := range b.Result.Trans {
		arr := make([][2]any, len(tr))
		for i, e := range tr {
			arr[i] = [2]any{e.T, e.Phase}
		}
		out[id] = arr
	}
	return out
}

func (s *Server) machineIndex(b *run.Bundle) []map[string]any {
	out := []map[string]any{}
	for _, m := range b.Result.Machines {
		out = append(out, map[string]any{
			"id": m.ID, "hostname": m.Hostname, "cabinet": m.Cabinet, "rackU": m.RackU,
			"model": m.Model, "phase": m.Phase, "phaseLabel": model.PhaseLabel(m.Phase),
			"fault": m.Fault, "faultLabel": labelOr(m.Fault),
			"verdict": m.Verdict, "doneAt": m.DoneAtS,
			"truth": m.Truth, "truthLabel": model.RootCauseLabel(m.Truth),
			"attribution": m.Attribution, "attributionLabel": labelOr(m.Attribution),
			"match": m.Matches, "held": m.Held,
		})
	}
	return out
}

// handleMachine serves /api/machine/{kind}/{id}.
func (s *Server) handleMachine(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/machine/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	b := s.all.Bundles[run.Kind(parts[0])]
	if b == nil {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rec := b.Result.Machine(id)
	if rec == nil {
		http.NotFound(w, r)
		return
	}
	m := s.all.Scenario.Machines[id-1]
	spec := s.all.Scenario.Templates[m.Model]
	po := s.all.Scenario.Procurement[m.Model]
	writeJSON(w, map[string]any{
		"record":      rec,
		"inventory":   m,
		"template":    spec,
		"procurement": po,
		"issues":      rec.Issues,
		"log":         rec.Log,
		"truthLabel":  model.RootCauseLabel(rec.Truth),
	})
}

// handleCabinet serves /api/cabinet/{kind}/{id}.
func (s *Server) handleCabinet(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/cabinet/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	b := s.all.Bundles[run.Kind(parts[0])]
	if b == nil {
		http.NotFound(w, r)
		return
	}
	cabID, err := strconv.Atoi(parts[1])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var hosts []*sim.MachineRecord
	for _, m := range b.Result.Machines {
		if m.Cabinet == cabID {
			hosts = append(hosts, m)
		}
	}
	faults := map[string]int{}
	for _, m := range hosts {
		code := m.Fault
		if code == "" && m.Verdict == "held" {
			code = m.AuditCode
		}
		if code != "" {
			faults[string(code)]++
		}
	}
	var faultRows []map[string]any
	for c, n := range faults {
		faultRows = append(faultRows, map[string]any{
			"code": c, "label": model.FaultLabel(model.FaultCode(c)), "count": n,
			"category": model.FaultCategory(model.FaultCode(c)),
		})
	}
	sort.Slice(faultRows, func(i, j int) bool {
		return faultRows[i]["count"].(int) > faultRows[j]["count"].(int)
	})
	sw := s.all.Scenario.Switches[fmt.Sprintf("TOR-%02d", cabID)]
	writeJSON(w, map[string]any{
		"cabinet": cabID, "name": spec.CabinetName(cabID), "switch": sw,
		"faults": faultRows,
		"hosts":  hosts,
	})
}

func (s *Server) serviceData(b *run.Bundle) map[string]any {
	return map[string]any{
		"svc":      b.Result.SvcLog,
		"leases":   b.Result.Leases,
		"peak":     b.Result.Peak,
		"switches": b.Result.Switches,
	}
}

// logStream returns a compact ordering of notable events for the live ticker.
func (s *Server) logStream(b *run.Bundle) []map[string]any {
	type ev struct {
		t   float64
		sev string
		l   string
		id  int
	}
	var all []ev
	for id, list := range b.Result.Events {
		for _, e := range list {
			if e.Severity == "error" || e.Severity == "warn" {
				all = append(all, ev{e.TimeS, e.Severity, e.Line, id})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t < all[j].t })
	out := []map[string]any{}
	for i, e := range all {
		if i > 400 {
			break
		}
		out = append(out, map[string]any{"t": e.t, "severity": e.sev, "line": e.l, "id": e.id})
	}
	return out
}

// ------------------------------------------------------------------------------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------------------------------------------------------------------------------

func truthBreakdown(sc *spec.Scenario) []map[string]any {
	counts := map[model.RootCause]int{}
	for _, m := range sc.Machines {
		counts[m.Truth]++
	}
	var rows []map[string]any
	for r, n := range counts {
		rows = append(rows, map[string]any{"code": string(r), "label": model.RootCauseLabel(r), "count": n})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["count"].(int) > rows[j]["count"].(int) })
	return rows
}

func fleetByModel(sc *spec.Scenario) []map[string]any {
	counts := map[string]int{}
	for _, m := range sc.Machines {
		counts[m.Model]++
	}
	var rows []map[string]any
	for _, mod := range spec.ModelOrder {
		rows = append(rows, map[string]any{"model": mod, "count": counts[mod]})
	}
	return rows
}

func labelOr(c model.FaultCode) string {
	if c == "" {
		return ""
	}
	return model.FaultLabel(c)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

// Serve starts the HTTP server.
func (s *Server) Serve(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("provisioning-sim dashboard on http://%s\n", addr)
	return srv.ListenAndServe()
}
