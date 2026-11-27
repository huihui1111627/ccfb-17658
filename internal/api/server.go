package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"sync"
	"time"

	"baremetal-rollout/internal/diagnose"
	"baremetal-rollout/internal/domain"
	"baremetal-rollout/internal/plan"
	"baremetal-rollout/internal/report"
	"baremetal-rollout/internal/sim"
)

//go:embed web/index.html
var webFS embed.FS

type MachineView struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	RackID          string              `json:"rackId"`
	RackIndex       int                 `json:"rackIndex"`
	UPosition       int                 `json:"uPosition"`
	SwitchPort      string              `json:"switchPort"`
	ModelID         string              `json:"modelId"`
	TemplateID      string              `json:"templateId"`
	MAC             string              `json:"mac"`
	AssignedIP      string              `json:"assignedIp"`
	Batch           int                 `json:"batch"`
	Stage           domain.Stage        `json:"stage"`
	StageLabel      string              `json:"stageLabel"`
	Status          domain.Status       `json:"status"`
	Progress        float64             `json:"progress"`
	LastError       string              `json:"lastError,omitempty"`
	Attempts        map[string]int      `json:"attempts"`
	Xfer            *domain.XferState   `json:"xfer,omitempty"`
	Truth           *domain.Issue       `json:"truth,omitempty"`
	Attribution     *domain.Attribution `json:"attribution,omitempty"`
	TransferQuality float64             `json:"transferQuality"`
	StartedAtMS     int64               `json:"startedAtMs"`
	StageAtMS       int64               `json:"stageAtMs"`
	FinishedAtMS    int64               `json:"finishedAtMs,omitempty"`
}

type StateResponse struct {
	Mode        string                  `json:"mode"`
	Paused      bool                    `json:"paused"`
	Speed       string                  `json:"speed"`
	StepMS      int64                   `json:"stepMs"`
	TimeMS      int64                   `json:"timeMs"`
	ActiveBatch int                     `json:"activeBatch"`
	ServerTime  string                  `json:"serverTime"`
	Summary     report.RunReport        `json:"summary"`
	Machines    []MachineView           `json:"machines"`
	StageOrder  []domain.Stage          `json:"stageOrder"`
	StageLabels map[domain.Stage]string `json:"stageLabels"`
	CodeLabels  map[string]string       `json:"codeLabels"`
}

type RackView struct {
	domain.Rack
	Total          int                   `json:"total"`
	Success        int                   `json:"success"`
	Blocked        int                   `json:"blocked"`
	Failed         int                   `json:"failed"`
	Active         int                   `json:"active"`
	StageCounts    map[domain.Stage]int  `json:"stageCounts"`
	ReasonCounts   map[string]int        `json:"reasonCounts"`
	DominantStage  domain.Stage          `json:"dominantStage"`
	DominantReason string                `json:"dominantReason"`
	Cluster        *diagnose.RackCluster `json:"cluster,omitempty"`
	Machines       []MachineView         `json:"machines,omitempty"`
}

type HardwareCheck struct {
	MachineID  string `json:"machineId"`
	Name       string `json:"name"`
	RackID     string `json:"rackId"`
	Model      string `json:"model"`
	OrderID    string `json:"orderId"`
	Category   string `json:"category"`
	Code       string `json:"code"`
	Title      string `json:"title"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	TruthCode  string `json:"truthCode"`
	TruthMatch bool   `json:"truthMatch"`
}

type Server struct {
	Base       *domain.Fleet
	Comparison report.Comparison
	Profile    plan.Profile

	mu     sync.Mutex
	live   *sim.Simulation
	paused bool
	speed  string
	stepMS int64

	hardwareChecks []HardwareCheck
}

func NewServer(base *domain.Fleet, comp report.Comparison, seed int64) (*Server, error) {
	profile := plan.DefaultProfile()
	live, err := sim.New(base, sim.ModeBatched, profile, seed)
	if err != nil {
		return nil, err
	}
	s := &Server{
		Base: base, Comparison: comp, Profile: profile,
		live: live, paused: false, speed: "normal", stepMS: 30_000,
		hardwareChecks: buildHardwareChecks(base),
	}
	go s.tickLoop()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/machines/{id}", s.handleMachine)
	mux.HandleFunc("GET /api/racks", s.handleRacks)
	mux.HandleFunc("GET /api/hardware-checks", s.handleHardwareChecks)
	mux.HandleFunc("GET /api/comparison", s.handleComparison)
	mux.HandleFunc("GET /api/plan", s.handlePlan)
	mux.HandleFunc("POST /api/live/reset", s.handleReset)
	mux.HandleFunc("POST /api/live/pause", s.handlePause)
	mux.HandleFunc("POST /api/live/resume", s.handleResume)
	mux.HandleFunc("POST /api/live/speed", s.handleSpeed)
	mux.HandleFunc("POST /api/live/finish", s.handleFinish)
	webSub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServerFS(webSub))
	return withJSONHeaders(mux)
}

func withJSONHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tickLoop() {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		if !s.paused && s.live != nil && !s.live.Done {
			s.live.Advance(s.stepMS)
			if s.live.Done {
				diagnose.ClusterRackIssues(s.live.Fleet)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clusters := []diagnose.RackCluster{}
	if s.live.Done {
		clusters = diagnose.ClusterRackIssues(s.live.Fleet)
	}
	summary := report.Summarize(s.live, clusters)
	resp := StateResponse{
		Mode: string(s.live.Mode), Paused: s.paused, Speed: s.speed, StepMS: s.stepMS,
		TimeMS: s.live.TimeMS, ActiveBatch: s.live.ActiveBatch,
		ServerTime: time.Now().Format(time.RFC3339), Summary: summary,
		Machines: makeViews(s.live.Fleet.Machines), StageOrder: domain.SortedStages(),
		StageLabels: domain.StageLabels, CodeLabels: domain.CodeLabels,
	}
	writeJSON(w, resp)
}

func (s *Server) handleMachine(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.live.Fleet.MachineByID(id)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	logs := append([]domain.LogEntry(nil), m.Logs...)
	domain.SortLogs(logs)
	writeJSON(w, map[string]any{
		"machine":    m,
		"logs":       logs,
		"stageLabel": domain.StageLabels[m.Stage],
	})
}

func (s *Server) handleRacks(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	clusters := diagnose.ClusterRackIssues(s.live.Fleet)
	clusterByRack := map[string]*diagnose.RackCluster{}
	for i := range clusters {
		clusterByRack[clusters[i].RackID] = &clusters[i]
	}
	byRack := map[string][]*domain.Machine{}
	for _, m := range s.live.Fleet.Machines {
		byRack[m.RackID] = append(byRack[m.RackID], m)
	}
	views := make([]RackView, 0, len(s.live.Fleet.Racks))
	for _, rack := range s.live.Fleet.Racks {
		ms := byRack[rack.ID]
		rv := RackView{
			Rack: rack, Total: len(ms), StageCounts: map[domain.Stage]int{},
			ReasonCounts: map[string]int{}, Cluster: clusterByRack[rack.ID],
		}
		for _, m := range ms {
			rv.StageCounts[m.Stage]++
			switch m.Status {
			case domain.StatusSuccess:
				rv.Success++
				rv.ReasonCounts[domain.CodeHealthy]++
			case domain.StatusBlocked:
				rv.Blocked++
				if m.Attribution != nil {
					rv.ReasonCounts[m.Attribution.Code]++
				}
			case domain.StatusFailed:
				rv.Failed++
				if m.Attribution != nil {
					rv.ReasonCounts[m.Attribution.Code]++
				}
			default:
				rv.Active++
			}
		}
		rv.DominantStage = dominantStage(rv.StageCounts)
		rv.DominantReason = dominantKey(rv.ReasonCounts)
		if r.URL.Query().Get("includeMachines") == "1" {
			rv.Machines = makeViews(ms)
		}
		views = append(views, rv)
	}
	s.mu.Unlock()
	writeJSON(w, views)
}

func (s *Server) handleHardwareChecks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.hardwareChecks)
}

func (s *Server) handleComparison(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Comparison)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Comparison.Plan)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	mode := sim.Mode(r.URL.Query().Get("mode"))
	if mode != sim.ModeStorm {
		mode = sim.ModeBatched
	}
	seed := s.Base.Seed
	live, err := sim.New(s.Base, mode, s.Profile, seed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.live = live
	s.paused = false
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "mode": mode})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
	writeJSON(w, map[string]any{"paused": true})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
	writeJSON(w, map[string]any{"paused": false})
}

func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	speed := r.URL.Query().Get("speed")
	step := int64(30_000)
	switch speed {
	case "slow":
		step = 5_000
	case "fast":
		step = 120_000
	default:
		speed = "normal"
		step = 30_000
	}
	s.mu.Lock()
	s.speed = speed
	s.stepMS = step
	s.mu.Unlock()
	writeJSON(w, map[string]any{"speed": speed, "stepMs": step})
}

func (s *Server) handleFinish(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.live.Run(report.DefaultHorizonMS, 1000)
	diagnose.ClusterRackIssues(s.live.Fleet)
	s.paused = true
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

func makeViews(ms []*domain.Machine) []MachineView {
	out := make([]MachineView, 0, len(ms))
	for _, m := range ms {
		progress := m.Progress
		if m.Status == domain.StatusSuccess {
			progress = 1
		}
		out = append(out, MachineView{
			ID: m.ID, Name: m.Name, RackID: m.RackID, RackIndex: m.RackIndex,
			UPosition: m.UPosition, SwitchPort: m.SwitchPort, ModelID: m.ModelID,
			TemplateID: m.TemplateID, MAC: m.MAC, AssignedIP: m.AssignedIP, Batch: m.Batch,
			Stage: m.Stage, StageLabel: domain.StageLabels[m.Stage], Status: m.Status,
			Progress: progress, LastError: m.LastError, Attempts: m.Attempts, Xfer: m.Xfer,
			Truth: m.Truth, Attribution: m.Attribution, TransferQuality: m.TransferQuality,
			StartedAtMS: m.StartedAtMS, StageAtMS: m.StageAtMS, FinishedAtMS: m.FinishedAtMS,
		})
	}
	return out
}

func dominantStage(counts map[domain.Stage]int) domain.Stage {
	type kv struct {
		k domain.Stage
		v int
	}
	var list []kv
	for k, v := range counts {
		if k == domain.StageDone {
			continue
		}
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	if len(list) == 0 {
		return domain.StageDone
	}
	return list[0].k
}

func dominantKey(counts map[string]int) string {
	best, n := "", 0
	for k, v := range counts {
		if k == domain.CodeHealthy {
			continue
		}
		if v > n {
			best, n = k, v
		}
	}
	return best
}

func buildHardwareChecks(f *domain.Fleet) []HardwareCheck {
	out := []HardwareCheck{}
	for _, m := range f.Machines {
		truth := ""
		if m.Truth != nil {
			truth = m.Truth.Code
		}
		for _, issue := range diagnose.ValidateHardware(m) {
			expected, actual := evidencePair(issue.Evidence)
			out = append(out, HardwareCheck{
				MachineID: m.ID, Name: m.Name, RackID: m.RackID, Model: m.ModelID,
				OrderID: m.Purchase.OrderID, Category: issue.Category, Code: issue.Code,
				Title: issue.Message, Expected: expected, Actual: actual,
				TruthCode: truth, TruthMatch: truth == issue.Code,
			})
		}
		if issue := diagnose.ValidateRAID(m, f.Templates); issue != nil {
			expected, actual := evidencePair(issue.Evidence)
			out = append(out, HardwareCheck{
				MachineID: m.ID, Name: m.Name, RackID: m.RackID, Model: m.ModelID,
				OrderID: m.Purchase.OrderID, Category: issue.Category, Code: issue.Code,
				Title: issue.Message, Expected: expected, Actual: actual,
				TruthCode: truth, TruthMatch: truth == issue.Code,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].MachineID < out[j].MachineID
	})
	return out
}

func evidencePair(e map[string]string) (string, string) {
	// Prefer explicit expected/actual fields; otherwise collapse evidence into
	// two columns in a stable enough way for the preflight table.
	if e == nil {
		return "", ""
	}
	if e["expected"] != "" || e["actual"] != "" {
		return e["expected"], e["actual"]
	}
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", ""
	}
	first := keys[0]
	second := ""
	if len(keys) > 1 {
		second = keys[1]
	}
	return first + "=" + e[first], second + "=" + e[second]
}

func writeJSON(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
