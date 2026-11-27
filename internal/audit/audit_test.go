package audit

import (
	"testing"

	"provisioning-sim/internal/model"
	"provisioning-sim/internal/spec"
)

func TestAuditHoldsAllSeededDefects(t *testing.T) {
	sc := spec.Build()
	au := Run(sc)
	// Every unit with a real defect must be held. Inventory and configuration
	// defects are all detectable before power-on.
	for _, m := range sc.Machines {
		if m.Truth == model.R_None {
			continue
		}
		// Image-corruption and install-script bugs are not preflight-detectable
		// (they live in the installer, not the unit); everything else should be.
		switch m.Truth {
		case model.R_ImageCorrupt, model.R_ImageSlow, model.R_InstallScript, model.R_DiskBad, model.R_MemEcc:
			continue
		}
		if res := au.Results[m.ID]; res == nil || !res.Hold {
			t.Errorf("machine %d (truth %q) should be held", m.ID, m.Truth)
		}
	}
}

func TestAuditNoFalsePositive(t *testing.T) {
	// Only seeded defects exist, so a held unit must have a truth.
	sc := spec.Build()
	au := Run(sc)
	for id, res := range au.Results {
		if res.Hold && sc.Machines[id-1].Truth == model.R_None {
			t.Errorf("machine %d held but has no defect", id)
		}
	}
}

func TestSwitchIssuesDetectedOnce(t *testing.T) {
	sc := spec.Build()
	au := Run(sc)
	// CAB-03 and CAB-14 have disabled boot VLANs; CAB-07 a low session cap.
	found := map[int]bool{}
	for _, si := range au.SwitchIssues {
		found[si.Cabinet] = true
	}
	for _, cab := range []int{3, 7, 14} {
		if !found[cab] {
			t.Errorf("cabinet %d switch issue not detected", cab)
		}
	}
	if len(au.SwitchIssues) != 3 {
		t.Errorf("got %d switch issues, want 3", len(au.SwitchIssues))
	}
}

func TestDominantRepairPriority(t *testing.T) {
	is := []Issue{
		{Code: model.F_BOOT_ORDER, Repair: RepairRemote},
		{Code: model.F_DIMM_SEAT, Repair: RepairPhysical},
	}
	code, rep := dominant(is)
	if code != model.F_DIMM_SEAT || rep != RepairPhysical {
		t.Fatalf("physical must win over remote, got %q/%q", code, rep)
	}
}
