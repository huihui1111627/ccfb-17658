package fleet

import "testing"

func TestGenerate800SeededFaults(t *testing.T) {
	f := Generate(800, 20261127)
	if f.Total != 800 || len(f.Machines) != 800 || len(f.Racks) != 20 {
		t.Fatalf("unexpected fleet shape: total=%d machines=%d racks=%d", f.Total, len(f.Machines), len(f.Racks))
	}

	counts := map[string]int{}
	for _, m := range f.Machines {
		if m.Truth == nil {
			counts["HEALTHY"]++
		} else {
			counts[m.Truth.Code]++
		}
	}
	want := map[string]int{
		"HEALTHY": 564, "SWITCH_VLAN_MISSING": 80, "BOOT_ORDER_WRONG": 24,
		"NIC_FIRMWARE_UNSUPPORTED": 28, "MEMORY_DIMM_MISSING": 32,
		"DISK_MODEL_MISMATCH": 40, "RAID_MODE_MISMATCH": 24,
		"TEMPLATE_NOT_APPLICABLE": 8,
	}
	for code, n := range want {
		if counts[code] != n {
			t.Fatalf("truth count %s = %d, want %d (all=%v)", code, counts[code], n, counts)
		}
	}
}
