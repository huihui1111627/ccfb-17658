package plan

import (
	"testing"

	"provisioning-sim/internal/model"
	"provisioning-sim/internal/spec"
)

func TestBottleneckRanking(t *testing.T) {
	sc := spec.Build()
	a := Analyze(sc, sc.Pools)
	if len(a.AllAtOnce) == 0 {
		t.Fatal("no bottlenecks")
	}
	// The image service is sized for ~500 hosts; at 800 units it should bind.
	top := a.AllAtOnce[0]
	if top.Capacity == 0 || top.PeakInUse < top.Capacity {
		t.Fatalf("expected the top bottleneck to be saturated, got %+v", top)
	}
	for i := range a.AllAtOnce {
		if a.AllAtOnce[i].Rank != i+1 {
			t.Errorf("ranking not sequential: %+v", a.AllAtOnce[i])
		}
	}
}

func TestRhythmEliminatesCapacityLoss(t *testing.T) {
	sc := spec.Build()
	a := Analyze(sc, sc.Pools)
	if a.Rhythm.PerWave <= 0 || len(a.Rhythm.Waves) == 0 {
		t.Fatal("rhythm not derived")
	}
	// The sweep must have found a zero-loss point and reported it.
	found := false
	for _, p := range a.Probe {
		if p.Capacity == 0 && p.PerWave == a.Rhythm.PerWave {
			found = true
		}
	}
	if !found {
		t.Errorf("recommended wave size %d has no zero-loss probe", a.Rhythm.PerWave)
	}
	if a.Rhythm.EstFailed != 0 {
		t.Errorf("recommended rhythm still loses %d to capacity", a.Rhythm.EstFailed)
	}
}

func TestWavesCoverFleet(t *testing.T) {
	count := 0
	for _, w := range buildWaves(50, 100) {
		count += w.Count
	}
	if count != spec.Total {
		t.Fatalf("waves cover %d, want %d", count, spec.Total)
	}
}

var _ = model.PhaseOff
