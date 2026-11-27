// Package report turns simulator results into the summaries, comparisons and
// ground-truth scorecards the UI and CLI present.
package report

import (
	"fmt"
	"sort"

	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/model"
	"provisioning-sim/internal/sim"
	"provisioning-sim/internal/spec"
)

// PhaseStat counts machines in one phase.
type PhaseStat struct {
	Phase model.Phase `json:"phase"`
	Label string      `json:"label"`
	Count int         `json:"count"`
}

// FaultStat counts machines stopped by one fault class.
type FaultStat struct {
	Code     model.FaultCode `json:"code"`
	Label    string          `json:"label"`
	Category string          `json:"category"`
	Count    int             `json:"count"`
}

// CabinetStat is one rack's aggregate health.
type CabinetStat struct {
	Cabinet  int    `json:"cabinet"`
	Name     string `json:"name"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Held     int    `json:"held"`
	TopFault string `json:"topFault,omitempty"`
	TopCount int    `json:"topCount"`
	Cluster  bool   `json:"cluster"` // one fault dominates → systemic
	Note     string `json:"note,omitempty"`
}

// Summary is the headline view of one run.
type Summary struct {
	Name         string                   `json:"name"`
	Label        string                   `json:"label"`
	MakespanH    float64                  `json:"makespanH"`
	Installed    int                      `json:"installed"`
	Failed       int                      `json:"failed"`
	Held         int                      `json:"held"`
	Capacity     int                      `json:"capacityLost"`
	Noncompliant int                      `json:"noncompliant"`
	SuccessRate  float64                  `json:"successRate"`
	Phases       []PhaseStat              `json:"phases"`
	Faults       []FaultStat              `json:"faults"`
	Cabinets     []CabinetStat            `json:"cabinets"`
	Peak         map[string]*sim.PeakStat `json:"peak"`
	// Triage is the operational cost of resolving what went wrong.
	Triage Triage `json:"triage"`
	// Scorecard is present only when ground truth is available.
	Score *Scorecard `json:"score,omitempty"`
}

// Triage quantifies how much hands-on work the batch leaves behind, which is
// the metric that decides whether the floor finishes in three days or one.
type Triage struct {
	// UniqueSignatures is how many distinct (phase, reason) symptoms appeared.
	UniqueSignatures int `json:"uniqueSignatures"`
	// BatchFixes is how many of those are systemic (≥3 units, same cause) and
	// therefore one root-cause fix clears the whole group.
	BatchFixes int `json:"batchFixes"`
	// BatchCovered is units whose fate a batch fix resolves.
	BatchCovered int `json:"batchCovered"`
	// SoloCases is units needing individual investigation.
	SoloCases int `json:"soloCases"`
	// EffortMin is estimated engineer-minutes: 4 per batch fix + 25 per solo.
	EffortMin float64 `json:"effortMin"`
	// SignatureGroups lists the clusters for display.
	Groups []TriageGroup `json:"groups"`
}

// TriageGroup is one symptom cluster.
type TriageGroup struct {
	Label    string `json:"label"`
	Phase    string `json:"phase"`
	Count    int    `json:"count"`
	Systemic bool   `json:"systemic"`
	Cabinet  int    `json:"cabinet,omitempty"`
}

// Scorecard is the ground-truth comparison.
type Scorecard struct {
	Exact       int     `json:"exact"`
	Symptom     int     `json:"symptom"`
	Miss        int     `json:"miss"`
	Unresolved  int     `json:"unresolved"`
	FalsePos    int     `json:"falsePositive"`
	TotalDefect int     `json:"totalDefects"`
	Precision   float64 `json:"precision"`
	Recall      float64 `json:"recall"`
	ExactRate   float64 `json:"exactRate"`
	// Pairs lists every seeded defect and the tool's answer.
	Pairs []VerdictPair `json:"pairs"`
	// Confusions is truth→answer counts, for the matrix view.
	Confusions []Confusion `json:"confusions"`
}

// VerdictPair is one machine's truth vs the tool.
type VerdictPair struct {
	ID         int    `json:"id"`
	Hostname   string `json:"hostname"`
	Cabinet    int    `json:"cabinet"`
	Model      string `json:"model"`
	Truth      string `json:"truth"`
	TruthCode  string `json:"truthCode"`
	Answer     string `json:"answer"`
	AnswerCode string `json:"answerCode"`
	Verdict    string `json:"verdict"`
	Match      string `json:"match"`
}

// Confusion is one cell of the truth/answer matrix.
type Confusion struct {
	Truth  string `json:"truth"`
	Answer string `json:"answer"`
	Count  int    `json:"count"`
}

// Build computes a Summary from a finished run.
func Build(r *sim.Result, label string, sc *spec.Scenario, withScore bool) *Summary {
	s := &Summary{Name: r.Name, Label: label, MakespanH: r.MakespanS / 3600, Peak: r.Peak}
	byPhase := map[model.Phase]int{}
	byFault := map[model.FaultCode]int{}
	cab := map[int]*CabinetStat{}

	for _, m := range r.Machines {
		byPhase[m.Phase]++
		if m.Fault != "" {
			byFault[m.Fault]++
		}
		cs := cab[m.Cabinet]
		if cs == nil {
			cs = &CabinetStat{Cabinet: m.Cabinet, Name: spec.CabinetName(m.Cabinet)}
			cab[m.Cabinet] = cs
		}
		cs.Total++
		switch m.Verdict {
		case "ok", "remediated":
			cs.Done++
		case "held":
			cs.Held++
		case "failed", "capacity":
			cs.Failed++
		}
	}
	s.Installed = r.Installed
	s.Failed = r.Failed
	s.Held = r.Held
	for _, m := range r.Machines {
		switch m.Verdict {
		case "capacity":
			s.Capacity++
		case "noncompliant":
			s.Noncompliant++
		}
	}
	if tot := len(r.Machines); tot > 0 {
		// Success counts compliant installs only: a unit installed with the
		// wrong memory is not a success.
		s.SuccessRate = float64(s.Installed-s.Noncompliant+0) / float64(tot)
	}
	for _, p := range model.Phases {
		s.Phases = append(s.Phases, PhaseStat{Phase: p, Label: model.PhaseLabel(p), Count: byPhase[p]})
	}
	var codes []model.FaultCode
	for c := range byFault {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return byFault[codes[i]] > byFault[codes[j]] })
	for _, c := range codes {
		s.Faults = append(s.Faults, FaultStat{
			Code: c, Label: model.FaultLabel(c), Category: model.FaultCategory(c), Count: byFault[c],
		})
	}

	// Cabinet clustering: a fault on ≥30% of a rack's units is systemic.
	for _, cs := range cab {
		cf := map[model.FaultCode]int{}
		for _, m := range r.Machines {
			if m.Cabinet == cs.Cabinet && m.Fault != "" {
				cf[m.Fault]++
			}
		}
		var top model.FaultCode
		for c, n := range cf {
			if n > cf[top] {
				top = c
			}
		}
		if top != "" {
			cs.TopFault = model.FaultLabel(top)
			cs.TopCount = cf[top]
			if cf[top]*10 >= cs.Total*3 {
				cs.Cluster = true
			}
		}
		// Name the rack-level cause from the scenario when present.
		if iss := sc.Switches[spec.CabinetName(cs.Cabinet)]; iss != nil {
			_ = iss
		}
		s.Cabinets = append(s.Cabinets, *cs)
	}
	sort.Slice(s.Cabinets, func(i, j int) bool { return s.Cabinets[i].Cabinet < s.Cabinets[j].Cabinet })

	if withScore {
		s.Score = Score(r, sc)
	}
	s.Triage = computeTriage(r)
	return s
}

// computeTriage measures the surprise workload: machines that were powered,
// did not cleanly finish, and must be chased down mid-batch. Preflight holds
// are excluded — they are already-known, scheduled work, not surprises.
//
// A signature shared by ≥3 machines is a systemic cause: one root-cause fix
// clears the group. Everything else is a one-off needing its own hands-on.
func computeTriage(r *sim.Result) Triage {
	type sigKey struct{ phase, reason string }
	sig := map[sigKey]*TriageGroup{}
	for _, m := range r.Machines {
		var phase, reason string
		switch m.Verdict {
		case "capacity":
			phase, reason = "network", "capacity"
		case "noncompliant":
			phase, reason = "install", string(m.Attribution)
		case "failed":
			if m.Fault == "" {
				continue
			}
			phase, reason = string(m.FaultPhase), string(m.Fault)
		default:
			// held / ok / remediated: no surprise, skip.
			continue
		}
		k := sigKey{phase, reason}
		g := sig[k]
		if g == nil {
			g = &TriageGroup{Label: reasonLabel(reason), Phase: phase}
			sig[k] = g
		}
		g.Count++
	}
	var groups []TriageGroup
	for _, g := range sig {
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Count > groups[j].Count })

	var t Triage
	t.UniqueSignatures = len(groups)
	for i := range groups {
		if groups[i].Count >= 3 {
			groups[i].Systemic = true
			t.BatchFixes++
			t.BatchCovered += groups[i].Count
		} else {
			t.SoloCases += groups[i].Count
		}
	}
	t.Groups = groups
	t.EffortMin = float64(t.BatchFixes)*4 + float64(t.SoloCases)*25
	return t
}

func reasonLabel(reason string) string {
	switch reason {
	case "capacity":
		return "资源不足(排队/地址)"
	case "":
		return "未知原因"
	}
	return model.FaultLabel(model.FaultCode(reason))
}

// Score computes truth vs the tool's attribution from each machine's
// already-classified match verdict.
func Score(r *sim.Result, sc *spec.Scenario) *Scorecard {
	c := &Scorecard{}
	conf := map[[2]string]int{}
	for _, m := range r.Machines {
		// Units with no defect and no adverse outcome are simply correct.
		if m.Truth == model.R_None && m.Matches == "n/a" {
			continue
		}
		answerCode := m.Attribution
		answerLabel := "—"
		switch {
		case answerCode != "":
			answerLabel = model.FaultLabel(answerCode)
		case m.Verdict == "noncompliant":
			answerLabel = "安装通过,配置不合规(未识别)"
		case m.Verdict == "held":
			answerLabel = model.FaultLabel(m.AuditCode)
		case m.Verdict == "capacity":
			answerLabel = "资源不足,主机自身问题未知"
		}
		c.Pairs = append(c.Pairs, VerdictPair{
			ID: m.ID, Hostname: m.Hostname, Cabinet: m.Cabinet, Model: m.Model,
			Truth: model.RootCauseLabel(m.Truth), TruthCode: string(m.Truth),
			Answer: answerLabel, AnswerCode: string(answerCode),
			Verdict: m.Verdict, Match: m.Matches,
		})
		conf[[2]string{string(m.Truth), string(answerCode)}]++

		switch m.Matches {
		case "exact":
			c.Exact++
		case "symptom":
			c.Symptom++
		case "miss":
			if m.Truth == model.R_None {
				c.FalsePos++
			} else {
				c.Miss++
			}
		case "unresolved":
			c.Unresolved++
		}
	}
	var keys [][2]string
	for k := range conf {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		c.Confusions = append(c.Confusions, Confusion{Truth: k[0], Answer: k[1], Count: conf[k]})
	}

	defects := 0
	for _, m := range sc.Machines {
		if m.Truth != model.R_None {
			defects++
		}
	}
	c.TotalDefect = defects
	if defects > 0 {
		c.ExactRate = float64(c.Exact) / float64(defects)
		c.Recall = float64(c.Exact+c.Symptom) / float64(defects)
	}
	flagged := c.Exact + c.Symptom + c.Miss + c.FalsePos
	if flagged > 0 {
		c.Precision = float64(c.Exact+c.Symptom) / float64(flagged)
	}
	sort.Slice(c.Pairs, func(i, j int) bool { return c.Pairs[i].ID < c.Pairs[j].ID })
	return c
}

// Compare formats the desktop comparison table.
func Compare(summaries []*Summary) string {
	var b []byte
	b = append(b, []byte(fmt.Sprintf("%-34s %8s %7s %7s %7s %7s\n", "方案", "用时(h)", "装好", "故障", "拦截", "资源失败"))...)
	for _, s := range summaries {
		b = append(b, []byte(fmt.Sprintf("%-34s %8.1f %7d %7d %7d %7d\n",
			s.Label, s.MakespanH, s.Installed, s.Failed, s.Held, s.Capacity))...)
	}
	return string(b)
}

// AuditSummary is the preflight report for the UI.
type AuditSummary struct {
	OK             int                   `json:"ok"`
	Held           int                   `json:"held"`
	ByRepair       map[audit.Repair]int  `json:"byRepair"`
	ByCode         []FaultStat           `json:"byCode"`
	ByField        []FieldCount          `json:"byField"`
	TemplateIssues []audit.TemplateIssue `json:"templateIssues"`
	SwitchIssues   []audit.SwitchIssue   `json:"switchIssues"`
}

// FieldCount counts held units by the field that failed.
type FieldCount struct {
	Field string `json:"field"`
	Count int    `json:"count"`
}

// BuildAudit summarises the audit.
func BuildAudit(f *audit.Fleet) *AuditSummary {
	a := &AuditSummary{
		OK: f.OK, Held: f.HeldCount, ByRepair: f.ByRepair,
		TemplateIssues: f.TemplateIssues, SwitchIssues: f.SwitchIssues,
	}
	var codes []model.FaultCode
	for c := range f.ByCode {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool { return f.ByCode[codes[i]] > f.ByCode[codes[j]] })
	for _, c := range codes {
		a.ByCode = append(a.ByCode, FaultStat{Code: c, Label: model.FaultLabel(c), Category: model.FaultCategory(c), Count: f.ByCode[c]})
	}
	for field, n := range f.ByField {
		a.ByField = append(a.ByField, FieldCount{Field: field, Count: n})
	}
	sort.Slice(a.ByField, func(i, j int) bool { return a.ByField[i].Count > a.ByField[j].Count })
	return a
}
