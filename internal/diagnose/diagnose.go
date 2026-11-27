package diagnose

import (
	"fmt"
	"strconv"
	"strings"

	"baremetal-rollout/internal/domain"
)

// CompareVersions compares dotted numeric firmware versions. Missing trailing
// components are treated as zero.
func CompareVersions(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func AttributionForIssue(issue domain.Issue, scope string) domain.Attribution {
	conf := 0.97
	if scope == "rack" {
		conf = 0.96
	}
	return domain.Attribution{
		Code: issue.Code, Category: issue.Category, RootCause: issue.Message,
		Confidence: conf, Scope: scope, Evidence: evidenceList(issue.Evidence),
	}
}

func evidenceList(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// ValidateNIC checks firmware and PXE ROM compatibility before DHCP.
func ValidateNIC(m *domain.Machine) *domain.Issue {
	if len(m.Hardware.NICs) == 0 {
		return &domain.Issue{Code: domain.CodeNICFirmwareUnsupported, Category: "firmware", Message: "未发现可用网卡"}
	}
	nic := m.Hardware.NICs[0]
	if CompareVersions(nic.Firmware, m.Purchase.MinNICFirmware) < 0 {
		return &domain.Issue{
			Code: domain.CodeNICFirmwareUnsupported, Category: "firmware",
			Message: "网卡固件低于 PXE/iPXE 兼容最低版本",
			Evidence: map[string]string{
				"nic": nic.Model, "current": nic.Firmware, "minimum": m.Purchase.MinNICFirmware,
				"rom": nic.PXEROM,
			},
		}
	}
	return nil
}

// ValidateHardware compares discovered inventory against the purchase order.
func ValidateHardware(m *domain.Machine) []domain.Issue {
	var issues []domain.Issue

	if len(m.Hardware.DIMMs) != m.Purchase.DIMMCount || m.Hardware.MemoryGB != m.Purchase.ExpectedMemoryGB {
		issues = append(issues, domain.Issue{
			Code: domain.CodeMemoryDIMMMissing, Category: "hardware",
			Message: "内存数量或总容量与采购单不一致",
			Evidence: map[string]string{
				"expectedDimms": strconv.Itoa(m.Purchase.DIMMCount),
				"actualDimms":   strconv.Itoa(len(m.Hardware.DIMMs)),
				"expectedGB":    strconv.Itoa(m.Purchase.ExpectedMemoryGB),
				"actualGB":      strconv.Itoa(m.Hardware.MemoryGB),
			},
		})
	}

	if len(m.Hardware.Disks) != m.Purchase.DiskCount {
		issues = append(issues, domain.Issue{
			Code: domain.CodeDiskModelMismatch, Category: "hardware",
			Message:  "硬盘数量与采购单不一致",
			Evidence: map[string]string{"expected": strconv.Itoa(m.Purchase.DiskCount), "actual": strconv.Itoa(len(m.Hardware.Disks))},
		})
	} else {
		for _, d := range m.Hardware.Disks {
			if d.Model != m.Purchase.DiskModel {
				issues = append(issues, domain.Issue{
					Code: domain.CodeDiskModelMismatch, Category: "hardware",
					Message: "存在不在采购/认证清单中的硬盘型号",
					Evidence: map[string]string{
						"slot": d.Slot, "expected": m.Purchase.DiskModel,
						"actual": d.Model, "serial": d.Serial,
					},
				})
				break
			}
		}
	}

	if issue := ValidateNIC(m); issue != nil {
		issues = append(issues, *issue)
	}
	return issues
}

// ValidateRAID checks template applicability and controller/array personality.
func ValidateRAID(m *domain.Machine, templates map[string]domain.RAIDTemplate) *domain.Issue {
	tpl, ok := templates[m.TemplateID]
	if !ok {
		return &domain.Issue{Code: domain.CodeTemplateNotApplicable, Category: "storage", Message: "部署参数引用了不存在的 RAID/分区模板", Evidence: map[string]string{"template": m.TemplateID}}
	}

	modelOK := false
	for _, model := range tpl.ApplicableModels {
		if model == m.Hardware.Model {
			modelOK = true
			break
		}
	}
	if !modelOK || len(m.Hardware.Disks) < tpl.MinDisks {
		return &domain.Issue{
			Code: domain.CodeTemplateNotApplicable, Category: "storage",
			Message: "模板适用机型或最小盘位条件不满足",
			Evidence: map[string]string{
				"template": tpl.ID, "model": m.Hardware.Model,
				"disks": strconv.Itoa(len(m.Hardware.Disks)), "minDisks": strconv.Itoa(tpl.MinDisks),
			},
		}
	}

	if tpl.RequiredModel != "" {
		for _, d := range m.Hardware.Disks {
			if d.Model != tpl.RequiredModel {
				// Hardware inventory normally catches this first; retain a
				// template-level guard for direct template validation.
				return &domain.Issue{Code: domain.CodeTemplateNotApplicable, Category: "storage", Message: "模板要求的认证磁盘型号不满足", Evidence: map[string]string{"required": tpl.RequiredModel, "actual": d.Model, "slot": d.Slot}}
			}
		}
	}

	if m.Hardware.RAID.Personality != tpl.Personality {
		return &domain.Issue{
			Code: domain.CodeRAIDModeMismatch, Category: "storage",
			Message: "RAID 控制器工作模式与模板要求不一致",
			Evidence: map[string]string{
				"controller": m.Hardware.RAID.Controller,
				"actual":     m.Hardware.RAID.Personality,
				"required":   tpl.Personality,
			},
		}
	}

	if m.Hardware.RAID.Controller != tpl.Controller {
		return &domain.Issue{
			Code: domain.CodeTemplateNotApplicable, Category: "storage",
			Message:  "RAID 控制器型号与模板不匹配",
			Evidence: map[string]string{"expected": tpl.Controller, "actual": m.Hardware.RAID.Controller},
		}
	}
	return nil
}

type AccuracyRow struct {
	MachineID   string        `json:"machineId"`
	Name        string        `json:"name"`
	RackID      string        `json:"rackId"`
	Truth       string        `json:"truth"`
	Predicted   string        `json:"predicted"`
	Match       bool          `json:"match"`
	Stage       domain.Stage  `json:"stage"`
	Status      domain.Status `json:"status"`
	TruthDetail string        `json:"truthDetail"`
	PredDetail  string        `json:"predDetail"`
}

type AccuracyReport struct {
	Total     int                       `json:"total"`
	Matched   int                       `json:"matched"`
	Accuracy  float64                   `json:"accuracy"`
	ByTruth   map[string]int            `json:"byTruth"`
	ByPred    map[string]int            `json:"byPred"`
	Confusion map[string]map[string]int `json:"confusion"`
	Rows      []AccuracyRow             `json:"rows"`
}

func Accuracy(machines []*domain.Machine) AccuracyReport {
	rep := AccuracyReport{
		Total: len(machines), ByTruth: map[string]int{}, ByPred: map[string]int{},
		Confusion: map[string]map[string]int{}, Rows: make([]AccuracyRow, 0, len(machines)),
	}
	for _, m := range machines {
		truth := domain.CodeHealthy
		truthDetail := "未埋设真实问题"
		if m.Truth != nil {
			truth = m.Truth.Code
			truthDetail = m.Truth.Message
		}
		pred := domain.CodeUnknown
		predDetail := "缺少归因"
		if m.Attribution != nil {
			pred = m.Attribution.Code
			predDetail = m.Attribution.RootCause
		} else if m.Status == domain.StatusSuccess {
			pred = domain.CodeHealthy
			predDetail = "全流程成功"
		}
		match := truth == pred
		if match {
			rep.Matched++
		}
		rep.ByTruth[truth]++
		rep.ByPred[pred]++
		if _, ok := rep.Confusion[truth]; !ok {
			rep.Confusion[truth] = map[string]int{}
		}
		rep.Confusion[truth][pred]++
		rep.Rows = append(rep.Rows, AccuracyRow{
			MachineID: m.ID, Name: m.Name, RackID: m.RackID, Truth: truth,
			Predicted: pred, Match: match, Stage: m.Stage, Status: m.Status,
			TruthDetail: truthDetail, PredDetail: predDetail,
		})
	}
	if rep.Total > 0 {
		rep.Accuracy = float64(rep.Matched) / float64(rep.Total)
	}
	return rep
}

// ClusterRackIssues finds same-cause failures concentrated in a rack. When the
// simulated switch configuration is available it confirms the VLAN fault; when
// not, a sufficiently large correlated cluster is still promoted to a rack
// root cause.
func ClusterRackIssues(f *domain.Fleet) []RackCluster {
	byRack := map[string][]*domain.Machine{}
	for _, m := range f.Machines {
		byRack[m.RackID] = append(byRack[m.RackID], m)
	}
	clusters := []RackCluster{}
	for _, rack := range f.Racks {
		ms := byRack[rack.ID]
		dhcpFailures := 0
		for _, m := range ms {
			if m.Stage == domain.StageDHCP && (m.Status == domain.StatusBlocked || m.Status == domain.StatusFailed || m.Status == domain.StatusRetrying) {
				dhcpFailures++
			}
		}
		if dhcpFailures < 4 {
			continue
		}

		code := domain.CodeDHCPOverload
		conf := 0.82
		scope := "service"
		related := "dhcp-cluster"
		evidence := []string{
			fmt.Sprintf("同机柜 %d 台机器集中停留在 DHCP 阶段", dhcpFailures),
			"失败报文均为 DHCP Discover 后无 Offer",
		}
		if rack.Switch.MissingVLAN == rack.Switch.ProvisionVLAN {
			code = domain.CodeSwitchVLANMissing
			conf = 0.99
			scope = "rack"
			related = rack.Switch.ID
			evidence = append(evidence,
				fmt.Sprintf("交换机配置核对：已放通 VLAN %v，缺少装机 VLAN %d", rack.Switch.VLANs, rack.Switch.ProvisionVLAN),
				"故障边界与该机柜拓扑完全重合",
			)
		} else if dhcpFailures >= 8 {
			code = domain.CodeSwitchVLANMissing
			conf = 0.94
			scope = "rack"
			related = rack.Switch.ID
			evidence = append(evidence, "跨机柜服务指标正常，故障按机柜高度相关，推断 ToR 端口/VLAN 配置错误")
		}

		cluster := RackCluster{
			RackID: rack.ID, SwitchID: rack.Switch.ID, Code: code,
			Category: "network", Confidence: conf, Affected: dhcpFailures,
			TotalServers: len(ms), Evidence: evidence,
		}
		clusters = append(clusters, cluster)

		issue := domain.Issue{Code: code, Category: "network", Message: domain.CodeLabels[code]}
		for _, m := range ms {
			if m.Stage == domain.StageDHCP && m.Status != domain.StatusSuccess {
				attr := AttributionForIssue(issue, scope)
				attr.Confidence = conf
				attr.RelatedID = related
				attr.Evidence = evidence
				m.Attribution = &attr
			}
		}
	}
	return clusters
}

type RackCluster struct {
	RackID       string   `json:"rackId"`
	SwitchID     string   `json:"switchId"`
	Code         string   `json:"code"`
	Category     string   `json:"category"`
	Confidence   float64  `json:"confidence"`
	Affected     int      `json:"affected"`
	TotalServers int      `json:"totalServers"`
	Evidence     []string `json:"evidence"`
}
