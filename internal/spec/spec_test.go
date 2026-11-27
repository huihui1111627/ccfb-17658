package spec

import (
	"testing"

	"provisioning-sim/internal/model"
)

func TestFleetSizeAndMix(t *testing.T) {
	sc := Build()
	if len(sc.Machines) != Total {
		t.Fatalf("got %d machines, want %d", len(sc.Machines), Total)
	}
	mods := map[string]int{}
	for _, m := range sc.Machines {
		mods[m.Model]++
	}
	// Exact procurement shares.
	want := map[string]int{"R760-A": 320, "R760-B": 240, "R660-C": 200, "XR400-D": 40}
	for k, v := range want {
		if mods[k] != v {
			t.Errorf("%s: got %d want %d", k, mods[k], v)
		}
	}
}

func TestDeterministic(t *testing.T) {
	a, b := Build(), Build()
	for i := range a.Machines {
		ma, mb := a.Machines[i], b.Machines[i]
		if ma.Truth != mb.Truth || ma.Serial != mb.Serial {
			t.Fatalf("machine %d differs between builds: %q vs %q", i, ma.Truth, mb.Truth)
		}
	}
}

func TestOneDefectPerUnit(t *testing.T) {
	// Rack/lot defects are allowed to coexist with nothing else, and per-unit
	// seeds are mutually exclusive by construction. Guard that a unit never
	// carries two conflicting physical signatures.
	sc := Build()
	for _, m := range sc.Machines {
		if m.Truth == model.R_DimmShort && m.Dimms.Count != sc.Procurement[m.Model].Dimms.Count-1 {
			t.Fatalf("machine %d marked dimm-short but has %d dimms", m.ID, m.Dimms.Count)
		}
		if m.Truth == model.R_SwitchOff {
			sw := sc.Switches[m.Cabinet.SwitchID]
			if sw == nil || sw.BootVLANEnabled {
				t.Fatalf("machine %d marked switch-off but switch is enabled", m.ID)
			}
		}
	}
}

func TestDefectRatesPlausible(t *testing.T) {
	sc := Build()
	defects := 0
	for _, m := range sc.Machines {
		if m.Truth != model.R_None {
			defects++
		}
	}
	// Between 15% and 35% of the fleet should carry a defect: enough to be
	// a real batch, not so much the run is unrealistic.
	if r := float64(defects) / float64(len(sc.Machines)); r < 0.15 || r > 0.35 {
		t.Fatalf("defect rate %.2f outside plausible band", r)
	}
}
