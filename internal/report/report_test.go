package report

import (
	"testing"

	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/sim"
	"provisioning-sim/internal/spec"
)

func TestScorecardBounded(t *testing.T) {
	sc := spec.Build()
	au := audit.Run(sc)
	r := sim.New(sc, au, sim.RunConfig{Preflight: true}, nil).Run()
	s := Build(r, "test", sc, true)
	sc2 := s.Score
	if sc2.ExactRate > 1 || sc2.ExactRate < 0 {
		t.Fatalf("exact rate out of range: %f", sc2.ExactRate)
	}
	if sc2.ExactRate > sc2.Recall {
		t.Fatalf("exact rate %f exceeds recall %f", sc2.ExactRate, sc2.Recall)
	}
	if sc2.Exact+sc2.Symptom+sc2.Miss+sc2.Unresolved > sc2.TotalDefect {
		t.Fatalf("match counts exceed total defects: %+v", sc2)
	}
}

func TestTriageLowerWithPreflight(t *testing.T) {
	sc := spec.Build()
	au := audit.Run(sc)
	naive := Build(sim.New(sc, au, sim.RunConfig{}, nil).Run(), "naive", sc, false)
	pre := Build(sim.New(sc, au, sim.RunConfig{Preflight: true}, nil).Run(), "pre", sc, false)
	if naive.Triage.EffortMin <= pre.Triage.EffortMin {
		t.Fatalf("preflight should reduce surprise workload: naive %.0f, pre %.0f",
			naive.Triage.EffortMin, pre.Triage.EffortMin)
	}
}

func TestSystemicClustersFlagged(t *testing.T) {
	sc := spec.Build()
	au := audit.Run(sc)
	r := sim.New(sc, au, sim.RunConfig{Preflight: true, Remediate: true}, nil).Run()
	_ = r
	// A naive run leaves the rack switch faults to show as clusters.
	naiveR := sim.New(sc, au, sim.RunConfig{}, nil).Run()
	s := Build(naiveR, "naive", sc, false)
	systemic := 0
	for _, g := range s.Triage.Groups {
		if g.Systemic {
			systemic++
		}
	}
	if systemic < 2 {
		t.Fatalf("expected systemic clusters from rack faults, got %d", systemic)
	}
}
