package model

// Phase identifies one step of the real power-on → provisioning → reboot
// sequence every server walks through.
type Phase string

const (
	PhaseOff     Phase = "off"     // never powered (held by the preflight audit)
	PhasePOST    Phase = "post"    // firmware self test + hardware inventory
	PhasePXE     Phase = "pxe"     // NIC option ROM: DHCP DISCOVER..ACK
	PhaseTFTP    Phase = "tftp"    // bootloader fetch
	PhaseHTTP    Phase = "http"    // installer image fetch
	PhaseInstall Phase = "install" // array config, partitioning, package copy
	PhaseReboot  Phase = "reboot"  // cold restart into the installed OS
	PhaseDone    Phase = "done"    // installed and booted
	PhaseFault   Phase = "fault"   // terminal failure / held for manual work
)

// Phases is the ordered list used by the UI for columns and sorting.
var Phases = []Phase{
	PhaseOff, PhasePOST, PhasePXE, PhaseTFTP, PhaseHTTP,
	PhaseInstall, PhaseReboot, PhaseDone, PhaseFault,
}

// BootPhases are the phases a host passes through after power-on.
var BootPhases = []Phase{PhasePOST, PhasePXE, PhaseTFTP, PhaseHTTP, PhaseInstall, PhaseReboot, PhaseDone}

// PhaseLabel is the Chinese UI label for a phase.
func PhaseLabel(p Phase) string {
	switch p {
	case PhaseOff:
		return "未上电/已拦截"
	case PhasePOST:
		return "固件自检"
	case PhasePXE:
		return "DHCP 获取地址"
	case PhaseTFTP:
		return "下载引导程序"
	case PhaseHTTP:
		return "拉取安装镜像"
	case PhaseInstall:
		return "分区装机"
	case PhaseReboot:
		return "重启进新系统"
	case PhaseDone:
		return "装机完成"
	case PhaseFault:
		return "故障停止"
	}
	return string(p)
}

// PhaseOrder returns the index of p in BootPhases, or -1.
func PhaseOrder(p Phase) int {
	for i, q := range BootPhases {
		if q == p {
			return i
		}
	}
	return -1
}

// Terminal reports whether no further state change is expected.
func Terminal(p Phase) bool { return p == PhaseDone || p == PhaseFault || p == PhaseOff }

// ServiceFor maps a phase to the shared service it stresses ("" when local).
func ServiceFor(p Phase) string {
	switch p {
	case PhasePXE:
		return "dhcp"
	case PhaseTFTP:
		return "tftp"
	case PhaseHTTP:
		return "http"
	case PhaseInstall:
		return "install"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Hardware
// ---------------------------------------------------------------------------

// Dimms is the memory population as physically fitted.
type Dimms struct {
	Count   int    `json:"count"`
	PerDIMM int    `json:"perDimmGB"`
	Model   string `json:"model"`
}

// Disks is the drive population as physically fitted.
type Disks struct {
	Count  int    `json:"count"`
	Kind   string `json:"kind"` // SSD | HDD | NVME
	Model  string `json:"model"`
	SizeGB int    `json:"sizeGB"`
}

// NICs is the network adapter and its option-ROM firmware.
type NICs struct {
	Model    string `json:"model"`
	Firmware string `json:"firmware"`
}

// Cabinet is the rack location, used for cluster attribution.
type Cabinet struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	SwitchID string `json:"switchId"`
}

// Machine is one physical server: what the BMC and the firmware report.
type Machine struct {
	ID             int      `json:"id"`
	Serial         string   `json:"serial"`
	Hostname       string   `json:"hostname"`
	Model          string   `json:"model"`
	Cabinet        Cabinet  `json:"cabinet"`
	RackU          int      `json:"rackU"`
	Dimms          Dimms    `json:"dimms"`
	Disks          Disks    `json:"disks"`
	NICs           NICs     `json:"nics"`
	BootOrder      []string `json:"bootOrder"`
	PxeEnabled     bool     `json:"pxeEnabled"`
	ControllerMode string   `json:"controllerMode"` // RAID | AHCI
	ControllerRAID string   `json:"controllerRaid"` // "none" or e.g. "RAID5:3"

	// Truth is the defect deliberately built into this unit. The simulator
	// uses it to decide physical behaviour; the analyzer never reads it.
	Truth     RootCause `json:"-"`
	TruthNote string    `json:"-"`
}

// RAIDTemplate describes the array the model must end up with.
type RAIDTemplate struct {
	Level          string `json:"level"` // RAID1 | RAID6 | RAID10
	MinMembers     int    `json:"minMembers"`
	ControllerMode string `json:"controllerMode"`
}

// Template is the per-model provisioning template.
type Template struct {
	Model               string       `json:"model"`
	RAID                RAIDTemplate `json:"raid"`
	RootDiskGB          int          `json:"rootDiskGB"`
	MinDiskCount        int          `json:"minDiskCount"`
	DiskKind            string       `json:"diskKind"`
	ImageName           string       `json:"imageName"`
	ImageGB             int          `json:"imageGB"`
	RequiresNICFirmware string       `json:"requiresNicFirmware"`
}

// Procurement is the purchase-order line the hardware audit compares against.
type Procurement struct {
	Model    string `json:"model"`
	Dimms    Dimms  `json:"dimms"`
	Disks    Disks  `json:"disks"`
	NICs     NICs   `json:"nics"`
	Quantity int    `json:"quantity"`
}

// ---------------------------------------------------------------------------
// Topology & shared resources
// ---------------------------------------------------------------------------

// Switch models the cabinet top-of-rack switch.
type Switch struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CabinetID       int    `json:"cabinetId"`
	Ports           int    `json:"ports"`
	BootVLAN        int    `json:"bootVlan"`
	BootVLANEnabled bool   `json:"bootVlanEnabled"`
	// MaxBootSessions caps concurrent in-flight boot sessions through this
	// switch (option-ROM / broadcast-storm protection).
	MaxBootSessions int `json:"maxBootSessions"`
}
