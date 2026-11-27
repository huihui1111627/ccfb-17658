package fleet

import (
	"fmt"
	"math/rand"

	"baremetal-rollout/internal/domain"
)

const ProvisionVLAN = 102

type modelSpec struct {
	id         string
	vendor     string
	role       string
	sku        string
	bios       string
	cpu        string
	dimmCount  int
	dimmModel  string
	dimmSizeGB int
	diskCount  int
	diskModel  string
	diskVendor string
	diskSizeGB int
	diskType   string
	diskFW     string
	nicModel   string
	nicFW      string
	minFW      string
	raidModel  string
	raidFW     string
	template   string
}

func modelSpecs() []modelSpec {
	return []modelSpec{
		{
			id: "CX-6248", vendor: "长虹/超聚变式 OEM", role: "通用计算", sku: "CX6248-G5-512-8N7",
			bios: "BIOS 2.8.1", cpu: "2× Xeon Gold 6548Y+ (64C)",
			dimmCount: 8, dimmModel: "M321R8GA0PB0-CWM", dimmSizeGB: 64,
			diskCount: 8, diskModel: "NVME7T64-G5", diskVendor: "Samsung", diskSizeGB: 7680, diskType: "NVMe", diskFW: "GDC5C02Q",
			nicModel: "CX-7 Lx 2×25GbE", nicFW: "28.39.1002", minFW: "28.39.1002",
			raidModel: "RAID-9560-8i", raidFW: "5.22.0-4126", template: "compute-standard",
		},
		{
			id: "ST-3608", vendor: "OceanStor OEM", role: "分布式存储", sku: "ST3608-G5-768-12S16",
			bios: "BIOS 2.7.4", cpu: "2× Xeon Silver 4514Y (32C)",
			dimmCount: 12, dimmModel: "M321R8GA0BB0-CWM", dimmSizeGB: 64,
			diskCount: 12, diskModel: "SAS16T-7K", diskVendor: "Seagate", diskSizeGB: 16000, diskType: "SAS", diskFW: "ST51",
			nicModel: "CX-6 2×25GbE", nicFW: "26.35.2000", minFW: "26.35.1014",
			raidModel: "RAID-9670-24i", raidFW: "5.24.0-4099", template: "storage-raid6",
		},
		{
			id: "GX-8840", vendor: "Galaxy GPU OEM", role: "GPU 训练节点", sku: "GX8840-H100-1TB-6N3",
			bios: "BIOS 3.1.0", cpu: "2× Xeon Platinum 8568Y+ (96C)",
			dimmCount: 16, dimmModel: "M321R8GA0PB0-CWM", dimmSizeGB: 64,
			diskCount: 6, diskModel: "NVME3T84-G5", diskVendor: "Kioxia", diskSizeGB: 3840, diskType: "NVMe", diskFW: "1VQDA01",
			nicModel: "CX-7 2×100GbE", nicFW: "28.40.1000", minFW: "28.39.1002",
			raidModel: "RAID-9560-16i", raidFW: "5.22.0-4126", template: "gpu-scratch",
		},
		{
			id: "MEM-9416", vendor: "Memory Optimized OEM", role: "内存数据库", sku: "MEM9416-G5-2TB-8N7",
			bios: "BIOS 2.9.2", cpu: "2× Xeon Platinum 8592+ (128C)",
			dimmCount: 16, dimmModel: "M321R96D0-XC", dimmSizeGB: 128,
			diskCount: 8, diskModel: "NVME7T64-G5", diskVendor: "Samsung", diskSizeGB: 7680, diskType: "NVMe", diskFW: "GDC5C02Q",
			nicModel: "CX-7 2×25GbE", nicFW: "28.39.1002", minFW: "28.39.1002",
			raidModel: "RAID-9560-8i", raidFW: "5.22.0-4126", template: "compute-standard",
		},
	}
}

func pickModel(r *rand.Rand) modelSpec {
	x := r.Intn(100)
	specs := modelSpecs()
	switch {
	case x < 45:
		return specs[0]
	case x < 70:
		return specs[1]
	case x < 88:
		return specs[2]
	default:
		return specs[3]
	}
}

func Generate(total int, seed int64) *domain.Fleet {
	if total <= 0 {
		total = 800
	}
	r := rand.New(rand.NewSource(seed))
	rackCount := (total + 39) / 40
	racks := buildRacks(rackCount)

	f := &domain.Fleet{
		Seed: seed, Total: total, Racks: racks,
		Templates: DefaultTemplates(),
		Machines:  make([]*domain.Machine, 0, total),
	}

	for i := 0; i < total; i++ {
		spec := pickModel(r)
		rackIndex := i / 40
		unit := i%40 + 1
		rack := racks[rackIndex]
		mac := fmt.Sprintf("02:42:0a:%02x:%02x:%02x", byte((i>>16)&0xff), byte((i>>8)&0xff), byte(i&0xff))
		hw := healthyHardware(i, spec, mac)
		expected := CloneHardware(hw)

		m := &domain.Machine{
			ID:         fmt.Sprintf("BM-%04d", i+1),
			Name:       fmt.Sprintf("node-%s-u%02d", rack.ID, unit),
			Serial:     hw.Serial,
			RackID:     rack.ID,
			RackIndex:  rackIndex,
			UPosition:  unit,
			SwitchPort: fmt.Sprintf("Ethernet1/%d", unit),
			ModelID:    spec.id,
			TemplateID: spec.template,
			MAC:        mac,
			BMCIP:      fmt.Sprintf("10.41.%d.%d", rackIndex+1, unit+10),
			BootOrder:  []string{"UEFI PXE IPv4", "UEFI HTTP Boot", "UEFI Local Disk"},
			// Stable node/ToR/TCP quality; slow nodes remain slow on retries.
			TransferQuality: 0.58 + r.Float64()*0.80,
			Hardware:        hw,
			Purchase: domain.PurchaseSpec{
				OrderID:          fmt.Sprintf("PO-2026-11-%03d", (i/100)+1),
				SKU:              spec.sku,
				Model:            spec.id,
				Role:             spec.role,
				Template:         spec.template,
				ExpectedMemoryGB: spec.dimmCount * spec.dimmSizeGB,
				DIMMCount:        spec.dimmCount,
				DIMMModel:        spec.dimmModel,
				DiskCount:        spec.diskCount,
				DiskModel:        spec.diskModel,
				NICModel:         spec.nicModel,
				MinNICFirmware:   spec.minFW,
				Expected:         expected,
			},
			Stage:    domain.StageQueued,
			Status:   domain.StatusPending,
			Attempts: map[string]int{},
		}
		f.Machines = append(f.Machines, m)
	}

	injectFaults(f, r)
	return f
}

func buildRacks(n int) []domain.Rack {
	bad := map[int]bool{}
	if n >= 2 {
		bad[int(float64(n)*0.30)] = true
		bad[int(float64(n)*0.70)] = true
	}
	racks := make([]domain.Rack, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("R%02d", i+1)
		vlans := []int{101, ProvisionVLAN, 200}
		sw := domain.RackSwitch{
			ID: fmt.Sprintf("sw-%s", id), RackID: id, Model: "CE6857-48S6CQ-EI",
			UplinkGbps: 200, ProvisionVLAN: ProvisionVLAN, VLANs: vlans, ConfigManaged: true,
		}
		if bad[i] {
			sw.VLANs = []int{101, 200}
			sw.MissingVLAN = ProvisionVLAN
		}
		racks = append(racks, domain.Rack{
			ID: id, Index: i, Switch: sw, PowerFeedKW: 12.8,
		})
	}
	return racks
}

func healthyHardware(i int, s modelSpec, mac string) domain.Hardware {
	dimms := make([]domain.DIMM, 0, s.dimmCount)
	for d := 0; d < s.dimmCount; d++ {
		channel := byte('A' + d/2)
		slot := fmt.Sprintf("DIMM_%c%d", channel, (d%2)+1)
		dimms = append(dimms, domain.DIMM{
			Slot: slot, Model: s.dimmModel, SizeGB: s.dimmSizeGB, SpeedMT: 5600,
		})
	}
	disks := make([]domain.Disk, 0, s.diskCount)
	for d := 0; d < s.diskCount; d++ {
		disks = append(disks, domain.Disk{
			Slot:   fmt.Sprintf("Bay-%02d", d+1),
			Model:  s.diskModel,
			Vendor: s.diskVendor,
			Serial: fmt.Sprintf("%s-%06d-%02d", s.diskType, i+1, d+1),
			SizeGB: s.diskSizeGB, Type: s.diskType, Firmware: s.diskFW,
		})
	}
	memory := s.dimmCount * s.dimmSizeGB
	return domain.Hardware{
		Vendor: s.vendor, Model: s.id, Serial: fmt.Sprintf("%s-SN%06d", s.id, i+1),
		BIOSVersion: s.bios, CPUModel: s.cpu, CPUSockets: 2, MemoryGB: memory,
		DIMMs: dimms, Disks: disks,
		NICs: []domain.NIC{{
			Name: "eth0", Model: s.nicModel, Firmware: s.nicFW, MinFirmware: s.minFW,
			PXEROM: "UEFI PXE ROM 2.5 / HTTPBoot 1.2", LinkSpeedGbps: 25, MAC: mac,
		}},
		RAID: domain.RAIDState{
			Controller: s.raidModel, Firmware: s.raidFW, Personality: "RAID",
			CachePolicy: "WriteBack", BatteryOK: true,
		},
	}
}

func injectFaults(f *domain.Fleet, r *rand.Rand) {
	// Rack-level switch/VLAN faults apply to every server in selected racks.
	for _, m := range f.Machines {
		if f.Racks[m.RackIndex].Switch.MissingVLAN == ProvisionVLAN {
			m.Truth = &domain.Issue{
				Code: domain.CodeSwitchVLANMissing, Category: "network",
				Message: "机柜交换机未放通装机 VLAN，DHCP Discover 被二层丢弃",
				Evidence: map[string]string{
					"rack": m.RackID, "missingVlan": fmt.Sprint(ProvisionVLAN),
					"switch": f.Racks[m.RackIndex].Switch.ID,
				},
			}
		}
	}

	candidates := make([]int, 0, len(f.Machines))
	for i, m := range f.Machines {
		if m.Truth == nil {
			candidates = append(candidates, i)
		}
	}
	perm := r.Perm(len(candidates))
	count := func(p float64) int {
		n := int(float64(f.Total)*p + 0.5)
		if n > len(candidates) {
			n = len(candidates)
		}
		return n
	}
	nBoot := count(0.030)
	nNIC := count(0.035)
	nMem := count(0.040)
	nDisk := count(0.050)
	nRAID := count(0.030)
	nTpl := count(0.010)

	cursor := 0
	take := func(n int) []int {
		out := make([]int, 0, n)
		for k := 0; k < n && cursor < len(perm); k++ {
			out = append(out, candidates[perm[cursor]])
			cursor++
		}
		return out
	}

	for _, idx := range take(nBoot) {
		m := f.Machines[idx]
		m.BootOrder = []string{"UEFI Local Disk", "UEFI Shell"}
		m.Truth = &domain.Issue{Code: domain.CodeBootOrderWrong, Category: "boot", Message: "PXE/IPv4 启动项被禁用或不在启动顺序首位", Evidence: map[string]string{"bootOrder": fmt.Sprint(m.BootOrder)}}
	}
	for _, idx := range take(nNIC) {
		m := f.Machines[idx]
		m.Hardware.NICs[0].Firmware = "20.35.1012"
		m.Hardware.NICs[0].PXEROM = "Legacy UNDI 11.4（不支持 iPXE/HTTPBoot 特性）"
		m.Truth = &domain.Issue{Code: domain.CodeNICFirmwareUnsupported, Category: "firmware", Message: "网卡固件/PXE ROM 过旧，无法完成新版 iPXE 引导握手", Evidence: map[string]string{"firmware": "20.35.1012", "minFirmware": m.Purchase.MinNICFirmware}}
	}
	for _, idx := range take(nMem) {
		m := f.Machines[idx]
		removed := m.Hardware.DIMMs[len(m.Hardware.DIMMs)-1]
		m.Hardware.DIMMs = m.Hardware.DIMMs[:len(m.Hardware.DIMMs)-1]
		m.Hardware.MemoryGB -= removed.SizeGB
		m.Truth = &domain.Issue{Code: domain.CodeMemoryDIMMMissing, Category: "hardware", Message: "实际 DIMM 数量/总容量少于采购单，疑似漏插内存", Evidence: map[string]string{"missingSlot": removed.Slot, "expected": fmt.Sprintf("%d 条/%dGB", m.Purchase.DIMMCount, m.Purchase.ExpectedMemoryGB), "actual": fmt.Sprintf("%d 条/%dGB", len(m.Hardware.DIMMs), m.Hardware.MemoryGB)}}
	}
	for _, idx := range take(nDisk) {
		m := f.Machines[idx]
		old := m.Hardware.Disks[2]
		wrongType := "SATA"
		wrongModel := "SATA8T-OEM"
		wrongSize := 8000
		if old.Type == "SAS" {
			wrongType = "NL-SAS"
			wrongModel = "SAS8T-OLD"
			wrongSize = 8000
		}
		m.Hardware.Disks[2] = domain.Disk{Slot: old.Slot, Model: wrongModel, Vendor: "Unknown/OEM", Serial: old.Serial, SizeGB: wrongSize, Type: wrongType, Firmware: "XX10"}
		m.Truth = &domain.Issue{Code: domain.CodeDiskModelMismatch, Category: "hardware", Message: "硬盘型号/容量与采购认证清单不一致", Evidence: map[string]string{"slot": old.Slot, "expectedModel": m.Purchase.DiskModel, "actualModel": wrongModel}}
	}
	for _, idx := range take(nRAID) {
		m := f.Machines[idx]
		m.Hardware.RAID.Personality = "JBOD/HBA"
		m.Hardware.RAID.ExistingVolumes = nil
		m.Truth = &domain.Issue{Code: domain.CodeRAIDModeMismatch, Category: "storage", Message: "RAID 控制器处于 JBOD/HBA 模式，无法按机型模板创建硬件阵列", Evidence: map[string]string{"actualPersonality": "JBOD/HBA", "requiredPersonality": "RAID"}}
	}
	for _, idx := range take(nTpl) {
		m := f.Machines[idx]
		wrong := "storage-raid6"
		if m.ModelID == "ST-3608" {
			wrong = "gpu-scratch"
		}
		m.TemplateID = wrong
		m.Truth = &domain.Issue{Code: domain.CodeTemplateNotApplicable, Category: "storage", Message: "所选 RAID/分区模板与机型、盘位或磁盘类型不匹配", Evidence: map[string]string{"template": wrong, "model": m.ModelID}}
	}
}

func CloneHardware(h domain.Hardware) domain.Hardware {
	out := h
	out.DIMMs = append([]domain.DIMM(nil), h.DIMMs...)
	out.Disks = append([]domain.Disk(nil), h.Disks...)
	out.NICs = append([]domain.NIC(nil), h.NICs...)
	out.RAID.ExistingVolumes = append([]domain.RAIDVolume(nil), h.RAID.ExistingVolumes...)
	for i := range out.RAID.ExistingVolumes {
		out.RAID.ExistingVolumes[i].DiskSlots = append([]string(nil), h.RAID.ExistingVolumes[i].DiskSlots...)
	}
	return out
}
