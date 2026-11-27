// Package audit is the pre-install hardware and configuration audit: it
// compares every unit against its purchase-order line and its model's
// provisioning template, and decides which units must be withheld.
package audit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"provisioning-sim/internal/model"
	"provisioning-sim/internal/spec"
)

// Repair tells the floor what kind of work clears the hold.
type Repair string

const (
	RepairPhysical Repair = "physical" // open the chassis / swap parts
	RepairRemote   Repair = "remote"   // BMC one-click config
	RepairSwitch   Repair = "switch"   // ToR / network change
	RepairTemplate Repair = "template" // fix the model template, not the server
)

// Issue is one failed check on one unit.
type Issue struct {
	Code   model.FaultCode `json:"code"`
	Field  string          `json:"field"`
	Want   string          `json:"want"`
	Got    string          `json:"got"`
	Repair Repair          `json:"repair"`
	Detail string          `json:"detail"`
}

// Result is the audit verdict for one unit.
type Result struct {
	MachineID int             `json:"machineId"`
	Issues    []Issue         `json:"issues"`
	Hold      bool            `json:"hold"`
	Code      model.FaultCode `json:"code,omitempty"`
	Repair    Repair          `json:"repair,omitempty"`
}

// TemplateIssue is a fleet-level template problem, found once per model.
type TemplateIssue struct {
	Model  string `json:"model"`
	Field  string `json:"field"`
	Detail string `json:"detail"`
	// Fixable means the template itself can be corrected and remediation
	// re-derives it from the purchase order (e.g. an over-specified disk
	// count, or a RAID level with too many members for the ordered drives).
	Fixable bool `json:"fixable"`
}

// SwitchIssue is a rack-level ToR problem that applies to every unit in it.
type SwitchIssue struct {
	Switch  string `json:"switch"`
	Cabinet int    `json:"cabinet"`
	Issue   Issue  `json:"issue"`
}

// Fleet is the audit output for the whole batch.
type Fleet struct {
	Results        map[int]*Result
	TemplateIssues []TemplateIssue
	SwitchIssues   []SwitchIssue
	OK             int
	HeldCount      int
	ByRepair       map[Repair]int
	ByCode         map[model.FaultCode]int
	ByField        map[string]int
}

// Withheld reports whether a unit must not be installed.
func (f *Fleet) Withheld(id int) (*Result, bool) {
	r, ok := f.Results[id]
	if !ok || !r.Hold {
		return r, false
	}
	return r, true
}

// SwitchIssueFor returns the rack-level issue for a cabinet, if any.
func (f *Fleet) SwitchIssueFor(cabID int) *SwitchIssue {
	for i := range f.SwitchIssues {
		if f.SwitchIssues[i].Cabinet == cabID {
			return &f.SwitchIssues[i]
		}
	}
	return nil
}

// Run audits the whole fleet.
func Run(sc *spec.Scenario) *Fleet {
	f := &Fleet{
		Results:  map[int]*Result{},
		ByRepair: map[Repair]int{},
		ByCode:   map[model.FaultCode]int{},
		ByField:  map[string]int{},
	}
	f.TemplateIssues = validateTemplates(sc)

	// Racks whose ToR is misconfigured fail as a block. The audit finds this
	// once per switch, not once per server.
	for _, swID := range sortedSwitchIDs(sc) {
		sw := sc.Switches[swID]
		hosts := countInCabinet(sc, sw.CabinetID)
		if !sw.BootVLANEnabled {
			f.SwitchIssues = append(f.SwitchIssues, SwitchIssue{
				Switch: sw.ID, Cabinet: sw.CabinetID,
				Issue: Issue{
					Code: model.F_SWITCH_OFF, Field: "switch.bootVlanEnabled",
					Want: "启用", Got: "停用", Repair: RepairSwitch,
					Detail: fmt.Sprintf("%s 的启动 VLAN %d 未放行,该柜 %d 台主机都拿不到 DHCP 应答",
						sw.ID, sw.BootVLAN, hosts),
				},
			})
		}
		if sw.MaxBootSessions < hosts {
			f.SwitchIssues = append(f.SwitchIssues, SwitchIssue{
				Switch: sw.ID, Cabinet: sw.CabinetID,
				Issue: Issue{
					Code: model.F_SWITCH_CAP, Field: "switch.maxBootSessions",
					Want: strconv.Itoa(hosts), Got: strconv.Itoa(sw.MaxBootSessions), Repair: RepairSwitch,
					Detail: fmt.Sprintf("%s 启动会话上限 %d 低于该柜主机数 %d,整柜同时启动会被丢弃",
						sw.ID, sw.MaxBootSessions, hosts),
				},
			})
		}
	}

	// A model-level template problem blocks every unit of that model.
	blockedModels := map[string]TemplateIssue{}
	for _, ti := range f.TemplateIssues {
		blockedModels[ti.Model] = ti
	}

	for _, m := range sc.Machines {
		f.Results[m.ID] = auditOne(sc, f, m, blockedModels[m.Model])
	}
	for _, r := range f.Results {
		if r.Hold {
			f.HeldCount++
			f.ByRepair[r.Repair]++
			f.ByCode[r.Code]++
			for _, is := range r.Issues {
				f.ByField[is.Field]++
			}
		} else {
			f.OK++
		}
	}
	return f
}

func auditOne(sc *spec.Scenario, f *Fleet, m *model.Machine, ti TemplateIssue) *Result {
	r := &Result{MachineID: m.ID}
	po := sc.Procurement[m.Model]
	tpl := sc.Templates[m.Model]

	add := func(is Issue) { r.Issues = append(r.Issues, is) }

	if ti.Model != "" {
		add(Issue{
			Code: model.F_TEMPLATE, Field: "template.model", Want: "与采购单一致",
			Got: "模板不适用", Repair: RepairTemplate, Detail: ti.Detail,
		})
	}
	if si := f.SwitchIssueFor(m.Cabinet.ID); si != nil {
		add(si.Issue)
	}

	// --- memory
	if m.Dimms.Count != po.Dimms.Count {
		add(Issue{
			Code: model.F_DIMM_SEAT, Field: "dimms.count",
			Want: strconv.Itoa(po.Dimms.Count), Got: strconv.Itoa(m.Dimms.Count), Repair: RepairPhysical,
			Detail: fmt.Sprintf("内存实插 %d 条,采购单 %d 条", m.Dimms.Count, po.Dimms.Count),
		})
	}
	if m.Dimms.Model != po.Dimms.Model {
		add(Issue{
			Code: model.F_DIMM_MODEL, Field: "dimms.model",
			Want: po.Dimms.Model, Got: m.Dimms.Model, Repair: RepairPhysical,
			Detail: fmt.Sprintf("内存料号 %s,采购单 %s", m.Dimms.Model, po.Dimms.Model),
		})
	}

	// --- disks: PO comparison first, then template applicability
	if m.Disks.Count != po.Disks.Count {
		add(Issue{
			Code: model.F_DISK_COUNT, Field: "disks.count",
			Want: strconv.Itoa(po.Disks.Count), Got: strconv.Itoa(m.Disks.Count), Repair: RepairPhysical,
			Detail: fmt.Sprintf("硬盘实插 %d 块,采购单 %d 块", m.Disks.Count, po.Disks.Count),
		})
	}
	if m.Disks.Model != po.Disks.Model || m.Disks.SizeGB != po.Disks.SizeGB {
		add(Issue{
			Code: model.F_DISK_MODEL, Field: "disks.model",
			Want: fmt.Sprintf("%s %dGB", po.Disks.Model, po.Disks.SizeGB),
			Got:  fmt.Sprintf("%s %dGB", m.Disks.Model, m.Disks.SizeGB), Repair: RepairPhysical,
			Detail: fmt.Sprintf("硬盘 %s %dGB,采购单 %s %dGB",
				m.Disks.Model, m.Disks.SizeGB, po.Disks.Model, po.Disks.SizeGB),
		})
	}
	if m.Disks.Kind != tpl.DiskKind {
		add(Issue{
			Code: model.F_TEMPLATE, Field: "template.diskKind", Want: tpl.DiskKind, Got: m.Disks.Kind,
			Repair: RepairPhysical,
			Detail: fmt.Sprintf("机型模板要求 %s 盘,实装 %s", tpl.DiskKind, m.Disks.Kind),
		})
	}
	if m.Disks.Count < tpl.MinDiskCount {
		add(Issue{
			Code: model.F_TEMPLATE, Field: "template.minDisks",
			Want: strconv.Itoa(tpl.MinDiskCount), Got: strconv.Itoa(m.Disks.Count), Repair: RepairPhysical,
			Detail: fmt.Sprintf("模板要求至少 %d 块盘,实装 %d 块,该机型模板不适用", tpl.MinDiskCount, m.Disks.Count),
		})
	}
	if m.Disks.Count < tpl.RAID.MinMembers {
		add(Issue{
			Code: model.F_TEMPLATE, Field: "template.raidMembers",
			Want: strconv.Itoa(tpl.RAID.MinMembers), Got: strconv.Itoa(m.Disks.Count), Repair: RepairPhysical,
			Detail: fmt.Sprintf("模板阵列 %s 需至少 %d 个成员盘,实装 %d 块", tpl.RAID.Level, tpl.RAID.MinMembers, m.Disks.Count),
		})
	}

	// --- NIC model and option-ROM firmware
	if m.NICs.Model != po.NICs.Model {
		add(Issue{
			Code: model.F_NIC_MODEL, Field: "nic.model", Want: po.NICs.Model, Got: m.NICs.Model,
			Repair: RepairPhysical,
			Detail: fmt.Sprintf("网卡型号 %s,采购单 %s", m.NICs.Model, po.NICs.Model),
		})
	}
	if model.CmpVersion(m.NICs.Firmware, tpl.RequiresNICFirmware) < 0 {
		add(Issue{
			Code: model.F_NIC_FIRMWARE, Field: "nic.firmware", Want: tpl.RequiresNICFirmware,
			Got: m.NICs.Firmware, Repair: RepairPhysical,
			Detail: fmt.Sprintf("网卡固件 %s 低于模板要求的 %s,无法完成 UEFI HTTP 引导",
				m.NICs.Firmware, tpl.RequiresNICFirmware),
		})
	}

	// --- controller configuration (remotely fixable)
	if m.ControllerMode != tpl.RAID.ControllerMode {
		add(issue(model.F_RAID_MODE, "controller.mode", tpl.RAID.ControllerMode, m.ControllerMode, RepairRemote,
			fmt.Sprintf("阵列控制器处于 %s 模式,模板要求 %s", m.ControllerMode, tpl.RAID.ControllerMode)))
	}
	if m.ControllerRAID != "none" {
		add(Issue{
			Code: model.F_RAID_CFG, Field: "controller.array", Want: "none", Got: m.ControllerRAID,
			Repair: RepairRemote,
			Detail: fmt.Sprintf("控制器残留阵列 %s,需清除后再装", m.ControllerRAID),
		})
	}

	// --- boot configuration (remotely fixable)
	if !m.PxeEnabled {
		add(Issue{
			Code: model.F_PXE_DISABLED, Field: "boot.pxeEnabled", Want: "启用", Got: "停用",
			Repair: RepairRemote, Detail: "网卡 option ROM 中 PXE 被禁用",
		})
	}
	if len(m.BootOrder) == 0 || m.BootOrder[0] != "pxe" {
		add(issue(model.F_BOOT_ORDER, "boot.order", "pxe 首项", strings.Join(m.BootOrder, ">"), RepairRemote,
			fmt.Sprintf("启动顺序 %s,首个设备不是网卡", strings.Join(m.BootOrder, ">"))))
	}

	if len(r.Issues) > 0 {
		r.Hold = true
		r.Code, r.Repair = dominant(r.Issues)
	}
	return r
}

func issue(code model.FaultCode, field, want, got string, rep Repair, detail string) Issue {
	return Issue{Code: code, Field: field, Want: want, Got: got, Repair: rep, Detail: detail}
}

// Severity ranks repairs: a physical problem already blocks the unit no
// matter what the network or template say, so it is reported first.
func severity(r Repair) int {
	switch r {
	case RepairPhysical:
		return 0
	case RepairSwitch:
		return 1
	case RepairTemplate:
		return 2
	case RepairRemote:
		return 3
	}
	return 4
}

// dominant picks the issue that best explains why a unit is not installable.
func dominant(is []Issue) (model.FaultCode, Repair) {
	best := is[0]
	for _, i := range is[1:] {
		if severity(i.Repair) < severity(best.Repair) {
			best = i
		}
	}
	return best.Code, best.Repair
}

// IssuesFor returns a machine's issues ordered so the blocking one reads first.
func IssuesFor(r *Result) []Issue {
	if r == nil {
		return nil
	}
	out := append([]Issue(nil), r.Issues...)
	sort.SliceStable(out, func(i, j int) bool { return severity(out[i].Repair) < severity(out[j].Repair) })
	return out
}

// validateTemplates checks each model's template against its purchase-order
// line. A template the ordered hardware cannot satisfy is a fleet-wide defect
// that blocks the whole model before a single machine boots.
func validateTemplates(sc *spec.Scenario) []TemplateIssue {
	var out []TemplateIssue
	var models []string
	for m := range sc.Templates {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, name := range models {
		tpl := sc.Templates[name]
		po := sc.Procurement[name]
		if tpl.MinDiskCount > po.Disks.Count {
			out = append(out, TemplateIssue{
				Model: name, Field: "minDiskCount", Fixable: true,
				Detail: fmt.Sprintf("模板要求至少 %d 块盘,采购单每台只有 %d 块;该机型 %d 台全部不适用,须先修模板",
					tpl.MinDiskCount, po.Disks.Count, po.Quantity),
			})
		}
		if tpl.RAID.MinMembers > po.Disks.Count {
			out = append(out, TemplateIssue{
				Model: name, Field: "raid.minMembers", Fixable: true,
				Detail: fmt.Sprintf("模板阵列 %s 需要 %d 个成员盘,采购单每台只有 %d 块",
					tpl.RAID.Level, tpl.RAID.MinMembers, po.Disks.Count),
			})
		}
		if tpl.DiskKind != po.Disks.Kind {
			out = append(out, TemplateIssue{
				Model: name, Field: "diskKind", Fixable: false,
				Detail: fmt.Sprintf("模板要求 %s 盘,采购单为 %s", tpl.DiskKind, po.Disks.Kind),
			})
		}
		if tpl.RootDiskGB > po.Disks.SizeGB {
			out = append(out, TemplateIssue{
				Model: name, Field: "rootDiskGB", Fixable: false,
				Detail: fmt.Sprintf("模板根分区 %dGB 大于单盘容量 %dGB", tpl.RootDiskGB, po.Disks.SizeGB),
			})
		}
	}
	return out
}

func sortedSwitchIDs(sc *spec.Scenario) []string {
	var ids []string
	for id := range sc.Switches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func countInCabinet(sc *spec.Scenario, cabID int) int {
	n := 0
	for _, m := range sc.Machines {
		if m.Cabinet.ID == cabID {
			n++
		}
	}
	return n
}
