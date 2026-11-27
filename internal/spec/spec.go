// Package spec builds the deterministic 800-machine scenario: topology,
// templates, purchase order, service pool sizing and defects.
package spec

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"provisioning-sim/internal/model"
)

const (
	CabinetCount = 20
	PerCabinet   = 40
	Total        = CabinetCount * PerCabinet
	Seed         = 0x5EED_800
)

// ModelOrder is the fleet mix, in procurement order.
var ModelOrder = []string{"R760-A", "R760-B", "R660-C", "XR400-D"}

var mixRatio = map[string]float64{
	"R760-A":  0.40,
	"R760-B":  0.30,
	"R660-C":  0.25,
	"XR400-D": 0.05,
}

// Defect rates per machine. They sum to ~0.12, i.e. about one unit in eight
// carries a physical or configuration defect. Rack-level defects are applied
// separately because a mis-cabled ToR takes out its whole cabinet.
const (
	rateBootOrder      = 0.014
	ratePxeDisabled    = 0.004
	rateRaidMode       = 0.014
	rateRaidConfig     = 0.006
	rateDimmShort      = 0.010
	rateDimmWrongModel = 0.005
	rateDiskWrongModel = 0.012
	rateDiskShort      = 0.006
	rateDiskBad        = 0.005
	rateNicOldFw       = 0.010
	rateNicWrongModel  = 0.003
	rateMemEcc         = 0.003
	rateImageSlow      = 0.004
	rateImageCorrupt   = 0.003
	rateInstallBug     = 0.004
)

// Cabinet-level defects: a defective NIC lot or DIMM lot shipped into a whole
// rack, plus switch problems.
const (
	nicLotCabinet  = 11 // firmware 18.4.0 on every unit, floor is 22.0.6
	dimmLotCabinet = 19 // UDIMM lot in RDIMM boards
)

// CabinetIssue records what is wrong with a rack's ToR.
type CabinetIssue string

const (
	CabOK        CabinetIssue = ""
	CabSwitchOff CabinetIssue = "switch.vlan-off"
	CabStormCap  CabinetIssue = "switch.storm-cap"
)

// CabinetPlan is the seeded cabinet-level layout.
var CabinetPlan = []struct {
	ID    int
	Issue CabinetIssue
	Note  string
}{
	{3, CabSwitchOff, "启动VLAN未从trunk放行"},
	{14, CabSwitchOff, "上联口配置回滚后未恢复"},
	{7, CabStormCap, "防广播风暴会话上限被误设为10"},
}

// Scenario is the complete immutable input to the simulator.
type Scenario struct {
	Machines    []*model.Machine
	Switches    map[string]*model.Switch
	Cabinets    map[int]model.Cabinet
	Templates   map[string]model.Template
	Procurement map[string]model.Procurement
	Pools       model.Pools
	Seed        uint64
}

// CabinetName renders "CAB-03".
func CabinetName(id int) string { return fmt.Sprintf("CAB-%02d", id) }

// SwitchFor returns the ToR of a cabinet.
func (s *Scenario) SwitchFor(cabID int) *model.Switch {
	return s.Switches[fmt.Sprintf("TOR-%02d", cabID)]
}

// Build constructs the scenario. The same seed always yields the same fleet.
func Build() *Scenario {
	rng := rand.New(rand.NewPCG(Seed, Seed>>32))
	sc := &Scenario{
		Templates:   Templates(),
		Procurement: Procurement(),
		Pools:       Pools(),
		Seed:        Seed,
		Cabinets:    make(map[int]model.Cabinet, CabinetCount),
		Switches:    make(map[string]*model.Switch, CabinetCount),
	}
	cabIssue := map[int]CabinetIssue{}
	for _, c := range CabinetPlan {
		cabIssue[c.ID] = c.Issue
	}

	// Cabinet and ToR build.
	for c := 1; c <= CabinetCount; c++ {
		cab := model.Cabinet{ID: c, Name: CabinetName(c), SwitchID: fmt.Sprintf("TOR-%02d", c)}
		sc.Cabinets[c] = cab
		sw := &model.Switch{
			ID: cab.SwitchID, Name: cab.SwitchID, CabinetID: c, Ports: 48,
			BootVLAN: 4000, BootVLANEnabled: true, MaxBootSessions: 48, // every port may boot at once by design
		}
		switch cabIssue[c] {
		case CabSwitchOff:
			sw.BootVLANEnabled = false
		case CabStormCap:
			sw.MaxBootSessions = 10
		}
		sc.Switches[sw.ID] = sw
	}

	// Model mix with exact integer shares, then shuffle so no cabinet is
	// homogeneous.
	modelIdx := make([]int, 0, Total)
	assigned := 0
	for i, name := range ModelOrder {
		n := int(float64(Total)*mixRatio[name] + 0.5)
		if i == len(ModelOrder)-1 {
			n = Total - assigned
		}
		assigned += n
		for range n {
			modelIdx = append(modelIdx, i)
		}
	}
	rng.Shuffle(len(modelIdx), func(i, j int) { modelIdx[i], modelIdx[j] = modelIdx[j], modelIdx[i] })

	po := sc.Procurement
	for i := 0; i < Total; i++ {
		id := i + 1
		cabID := (id-1)/PerCabinet + 1
		rackU := 42 - (id-1)%PerCabinet
		m := &model.Machine{
			ID: id, Serial: fmt.Sprintf("SN%05d", 20000+id),
			Hostname: fmt.Sprintf("cmp-%02d-%02d", cabID, (id-1)%PerCabinet+1),
			Model:    ModelOrder[modelIdx[i]],
			Cabinet:  sc.Cabinets[cabID], RackU: rackU,
		}
		line := po[m.Model]
		m.Dimms, m.Disks, m.NICs = line.Dimms, line.Disks, line.NICs
		m.BootOrder = []string{"pxe", "disk"}
		m.PxeEnabled = true
		m.ControllerMode = "RAID"
		m.ControllerRAID = "none"

		// Whole-rack lots: the physical defect is applied so the whole cabinet
		// is uniformly affected (the "成片" pattern the tool must surface).
		if cabID == nicLotCabinet {
			m.NICs.Firmware = "18.4.0"
		}
		if cabID == dimmLotCabinet {
			m.Dimms.Model = "DDR5-4800-32G-UDIMM"
		}

		// A whole-rack defect is every unit's own truth: a host in CAB-03
		// really does have an unusable boot network, even though the root
		// cause lives on the switch. Whole-rack defects are homogeneous by
		// nature — the same bad part or the same misconfigured ToR — so those
		// cabinets carry no per-unit seed. That keeps one defect per unit and
		// attribution unambiguous.
		switch {
		case cabIssue[cabID] == CabSwitchOff:
			m.Truth, m.TruthNote = model.R_SwitchOff, "所在机柜 "+CabinetName(cabID)+" 的ToR未放行启动VLAN"
		case cabIssue[cabID] == CabStormCap:
			m.Truth, m.TruthNote = model.R_SwitchCap, "所在机柜 "+CabinetName(cabID)+" 的ToR启动会话上限过低"
		case cabID == nicLotCabinet:
			m.Truth, m.TruthNote = model.R_NicOldFw, "该柜整批网卡固件18.4.0,低于模板要求"
		case cabID == dimmLotCabinet:
			m.Truth, m.TruthNote = model.R_DimmWrongModel, "该柜整批内存为UDIMM,采购单为RDIMM"
		default:
			m.Truth, m.TruthNote = seedMachine(rng, m)
		}

		sc.Machines = append(sc.Machines, m)
	}
	return sc
}

// seedMachine draws at most one defect and makes it physical. One defect per
// host keeps attribution unambiguous: when two overlap, no observation can
// say which produced a symptom, and the scorecard would measure ambiguity
// rather than accuracy.
func seedMachine(rng *rand.Rand, m *model.Machine) (model.RootCause, string) {
	type draw struct {
		rate float64
		run  func() (model.RootCause, string)
	}
	draws := []draw{
		{rateBootOrder, func() (model.RootCause, string) {
			m.BootOrder = []string{"disk", "pxe"}
			return model.R_BootOrder, "启动顺序为 disk,pxe"
		}},
		{ratePxeDisabled, func() (model.RootCause, string) {
			m.PxeEnabled = false
			return model.R_PxeDisabled, "option ROM 中 PXE 被关闭"
		}},
		{rateRaidMode, func() (model.RootCause, string) {
			m.ControllerMode = "AHCI"
			return model.R_RaidMode, "阵列控制器未切到RAID模式"
		}},
		{rateRaidConfig, func() (model.RootCause, string) {
			m.ControllerRAID = "RAID5:3"
			return model.R_RaidConfig, "控制器上残留试机阵列RAID5"
		}},
		{rateDimmShort, func() (model.RootCause, string) {
			m.Dimms.Count--
			return model.R_DimmShort, fmt.Sprintf("实插%d条,应插%d条", m.Dimms.Count+1, m.Dimms.Count)
		}},
		{rateDimmWrongModel, func() (model.RootCause, string) {
			m.Dimms.Model = "DDR5-5600-32G-REFURB"
			return model.R_DimmWrongModel, "混插翻新条,料号不符"
		}},
		{rateDiskWrongModel, func() (model.RootCause, string) {
			m.Disks.SizeGB /= 2
			m.Disks.Model = "SPARE-GEN4-X"
			return model.R_DiskWrongModel, "备件盘,容量与料号不符"
		}},
		{rateDiskShort, func() (model.RootCause, string) {
			m.Disks.Count--
			return model.R_DiskShort, fmt.Sprintf("实插%d块", m.Disks.Count+1)
		}},
		{rateDiskBad, func() (model.RootCause, string) {
			return model.R_DiskBad, "一块盘SMART预失效,写入时掉线"
		}},
		{rateNicOldFw, func() (model.RootCause, string) {
			m.NICs.Firmware = "18.4.0"
			return model.R_NicOldFw, "网卡固件18.4.0低于模板要求"
		}},
		{rateNicWrongModel, func() (model.RootCause, string) {
			if m.NICs.Model == "X710-10G" {
				m.NICs.Model = "BCM57414-25G"
			} else {
				m.NICs.Model = "X710-10G"
			}
			return model.R_NicWrongModel, "网卡型号与采购单不符"
		}},
		{rateMemEcc, func() (model.RootCause, string) {
			return model.R_MemEcc, "一条内存高位ECC错误,压测时触发"
		}},
		{rateImageSlow, func() (model.RootCause, string) {
			return model.R_ImageSlow, "该机柜到镜像服务器链路丢包"
		}},
		{rateImageCorrupt, func() (model.RootCause, string) {
			return model.R_ImageCorrupt, "镜像文件校验不通过"
		}},
		{rateInstallBug, func() (model.RootCause, string) {
			return model.R_InstallScript, "该机型kickstart分区段引用不存在的设备"
		}},
	}
	total := 0.0
	for _, d := range draws {
		total += d.rate
	}
	if rng.Float64() >= total {
		return model.R_None, ""
	}
	x := rng.Float64() * total
	for _, d := range draws {
		if x < d.rate {
			return d.run()
		}
		x -= d.rate
	}
	return model.R_None, ""
}

// SortedMachines returns the fleet in rack order.
func (s *Scenario) SortedMachines() []*model.Machine {
	out := append([]*model.Machine(nil), s.Machines...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Templates returns the provisioning template for each model.
func Templates() map[string]model.Template {
	return map[string]model.Template{
		"R760-A": {
			Model: "R760-A", RAID: model.RAIDTemplate{Level: "RAID1", MinMembers: 2, ControllerMode: "RAID"},
			RootDiskGB: 480, MinDiskCount: 2, DiskKind: "SSD",
			ImageName: "os-image-r760a-11g", ImageGB: 11, RequiresNICFirmware: "22.0.6",
		},
		"R760-B": {
			Model: "R760-B", RAID: model.RAIDTemplate{Level: "RAID6", MinMembers: 4, ControllerMode: "RAID"},
			RootDiskGB: 960, MinDiskCount: 8, DiskKind: "HDD",
			ImageName: "os-image-r760b-8g", ImageGB: 8, RequiresNICFirmware: "22.0.6",
		},
		"R660-C": {
			Model: "R660-C", RAID: model.RAIDTemplate{Level: "RAID1", MinMembers: 2, ControllerMode: "RAID"},
			RootDiskGB: 240, MinDiskCount: 2, DiskKind: "SSD",
			ImageName: "os-image-r660c-6g", ImageGB: 6, RequiresNICFirmware: "20.5.0",
		},
		"XR400-D": {
			Model: "XR400-D", RAID: model.RAIDTemplate{Level: "RAID10", MinMembers: 4, ControllerMode: "RAID"},
			RootDiskGB: 960, MinDiskCount: 4, DiskKind: "NVME",
			ImageName: "os-image-xr400d-13g", ImageGB: 13, RequiresNICFirmware: "24.1.0",
		},
	}
}

// Procurement returns the purchase-order lines the audit compares against.
func Procurement() map[string]model.Procurement {
	return map[string]model.Procurement{
		"R760-A": {
			Model:    "R760-A",
			Dimms:    model.Dimms{Count: 8, PerDIMM: 32, Model: "DDR5-4800-32G-RDIMM"},
			Disks:    model.Disks{Count: 2, Kind: "SSD", Model: "PM9A3-480G", SizeGB: 480},
			NICs:     model.NICs{Model: "BCM57414-25G", Firmware: "22.0.6"},
			Quantity: 320,
		},
		"R760-B": {
			Model:    "R760-B",
			Dimms:    model.Dimms{Count: 12, PerDIMM: 32, Model: "DDR5-4800-32G-RDIMM"},
			Disks:    model.Disks{Count: 8, Kind: "HDD", Model: "ST16000NM-16T", SizeGB: 16000},
			NICs:     model.NICs{Model: "BCM57414-25G", Firmware: "22.0.6"},
			Quantity: 240,
		},
		"R660-C": {
			Model:    "R660-C",
			Dimms:    model.Dimms{Count: 6, PerDIMM: 16, Model: "DDR5-4800-16G-RDIMM"},
			Disks:    model.Disks{Count: 2, Kind: "SSD", Model: "PM9A3-240G", SizeGB: 240},
			NICs:     model.NICs{Model: "X710-10G", Firmware: "20.5.0"},
			Quantity: 200,
		},
		"XR400-D": {
			Model:    "XR400-D",
			Dimms:    model.Dimms{Count: 16, PerDIMM: 64, Model: "DDR5-4800-64G-RDIMM"},
			Disks:    model.Disks{Count: 4, Kind: "NVME", Model: "PM1733-960G", SizeGB: 960},
			NICs:     model.NICs{Model: "BCM57508-100G", Firmware: "24.1.0"},
			Quantity: 40,
		},
	}
}

// Pools returns the shared service resources. Sized for roughly 500 booting
// hosts, not 800: the tender never upsized them, and the analysis reports
// which one binds first.
func Pools() model.Pools {
	return model.Pools{
		// Sized for ~500 booting hosts. 800 units at once outruns all of it.
		DHCPLeases:   512,
		DHCPSeats:    32,
		DHCPSeatMs:   18,
		TFTPStreams:  32,
		TFTPMBps:     8,
		BootloaderMB: 48,
		// 2×10GbE bonded image server ≈ 200 MB/s aggregate, 24 streams.
		HTTPStreams:    24,
		HTTPMBps:       200,
		HTTPStreamMBps: 40,
		InstallShare:   64,
	}
}
