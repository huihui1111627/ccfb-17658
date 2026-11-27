package sim

import (
	"testing"

	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/model"
	"provisioning-sim/internal/spec"
)

func setup() (*spec.Scenario, *audit.Fleet) {
	sc := spec.Build()
	return sc, audit.Run(sc)
}

func TestAllHostsTerminal(t *testing.T) {
	sc, au := setup()
	r := New(sc, au, RunConfig{}, nil).Run()
	for _, m := range r.Machines {
		if !model.Terminal(m.Phase) {
			t.Errorf("machine %d ended in non-terminal phase %q", m.ID, m.Phase)
		}
	}
}

func TestPhaseOrderIsMonotone(t *testing.T) {
	sc, _ := setup()
	r := New(sc, nil, RunConfig{Name: "clean"}, nil).Run()
	// A healthy host's transitions must never go backwards through the boot
	// sequence (retries aside, which re-enter DHCP from the phase it was in).
	for _, m := range r.Machines {
		tr := r.Trans[m.ID]
		last := -1
		for _, e := range tr {
			o := model.PhaseOrder(e.Phase)
			if o < 0 {
				continue
			}
			if o < last && e.Phase != model.PhasePXE && e.Phase != model.PhaseTFTP && e.Phase != model.PhaseHTTP {
				// Retry paths legitimately rewind to PXE/TFTP/HTTP.
				t.Errorf("machine %d rewound to %q", m.ID, model.PhaseLabel(e.Phase))
			}
			if o > last {
				last = o
			}
		}
	}
}

func TestBatchedRunHasNoCapacityLoss(t *testing.T) {
	sc, au := setup()
	waves := make([]Wave, 0, 5)
	for i := 0; i < 5; i++ {
		waves = append(waves, Wave{AtS: float64(i) * 3600, Count: 160})
	}
	r := New(sc, au, RunConfig{Preflight: true, StaggerS: 0.5}, waves).Run()
	capLoss := 0
	for _, m := range r.Machines {
		if m.Verdict == "capacity" {
			capLoss++
		}
	}
	if capLoss != 0 {
		t.Fatalf("batched run had %d capacity-caused failures, want 0", capLoss)
	}
}

func TestAllAtOnceExhaustsCapacity(t *testing.T) {
	sc, au := setup()
	r := New(sc, au, RunConfig{}, nil).Run()
	capLoss := 0
	for _, m := range r.Machines {
		if m.Verdict == "capacity" {
			capLoss++
		}
	}
	if capLoss < 50 {
		t.Fatalf("all-at-once should show capacity loss, got %d", capLoss)
	}
}

func TestPreflightRaisesAttributionAccuracy(t *testing.T) {
	sc, au := setup()
	naive := New(sc, au, RunConfig{}, nil).Run()
	pre := New(sc, au, RunConfig{Preflight: true}, nil).Run()

	score := func(r *Result) (exact, defects int) {
		for _, m := range r.Machines {
			if m.Truth != model.R_None {
				defects++
			}
			if m.Matches == "exact" {
				exact++
			}
		}
		return
	}
	nE, d := score(naive)
	pE, _ := score(pre)
	if d == 0 {
		t.Fatal("no defects in scenario")
	}
	if float64(pE)/float64(d) <= float64(nE)/float64(d) {
		t.Fatalf("preflight did not improve attribution: naive %d/%d, preflight %d/%d", nE, d, pE, d)
	}
}

func TestLeasesNeverExceedPool(t *testing.T) {
	sc, au := setup()
	r := New(sc, au, RunConfig{}, nil).Run()
	pool := sc.Pools.DHCPLeases
	for _, l := range r.Leases {
		if l.InUse > pool {
			t.Fatalf("lease occupancy %d exceeded pool %d at t=%.1f", l.InUse, pool, l.T)
		}
	}
}

func TestEveryEventHasTime(t *testing.T) {
	sc, au := setup()
	r := New(sc, au, RunConfig{}, nil).Run()
	for id, evs := range r.Events {
		for i := 1; i < len(evs); i++ {
			if evs[i].TimeS < evs[i-1].TimeS {
				t.Fatalf("machine %d event times not monotone", id)
			}
		}
	}
}
