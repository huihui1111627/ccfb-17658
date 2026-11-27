// Package run wires the scenario, audit, planner and simulator into the
// named strategy runs the UI and CLI present.
package run

import (
	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/plan"
	"provisioning-sim/internal/report"
	"provisioning-sim/internal/sim"
	"provisioning-sim/internal/spec"
)

// Kind selects a named strategy.
type Kind string

const (
	KindNaive     Kind = "naive"     // today: audit off, every unit powered at once
	KindPreflight Kind = "preflight" // audit on, hold mismatches, one wave
	KindRhythm    Kind = "rhythm"    // preflight + audit guidance + staged waves
	KindFull      Kind = "full"      // preflight + remote remediation + staged waves
)

// Brief is one named comparison scenario.
type Brief struct {
	Kind  Kind
	Label string
	Note  string
}

// Library is the standard comparison set.
var Library = []Brief{
	{KindNaive, "今日做法:全量上电,不核对",
		"800 台一次性上电,硬件/配置问题在上电后才暴露,靠人逐台接显示器排查。"},
	{KindPreflight, "装机前核对,问题机拦下",
		"预检核对内存/硬盘/网卡/固件与采购单,配错的、模板不适用的标出并拦下;合格机全量上电。"},
	{KindRhythm, "核对 + 分批上电",
		"在核对基础上,按仿真得出的瓶颈节奏分批上电,避免地址池/引导服务/镜像服务被压垮。"},
	{KindFull, "核对 + 远程修复 + 分批",
		"对可远程修复的(启动顺序、网卡PXE、阵列模式、残留阵列)经BMC下发修正后分批上电;物理问题仍拦下。"},
}

// Bundle is everything one batch run produces.
type Bundle struct {
	Brief   Brief           `json:"brief"`
	Result  *sim.Result     `json:"-"`
	Summary *report.Summary `json:"summary"`
	Waves   []sim.Wave      `json:"waves"`
	Config  sim.RunConfig   `json:"config"`
}

// All holds the full analysis for the scenario.
type All struct {
	Scenario *spec.Scenario
	Audit    *audit.Fleet
	Analysis *plan.Analysis
	Bundles  map[Kind]*Bundle
	Order    []Kind
}

// Compute builds the scenario, audit, plan and all four strategy runs.
func Compute() *All {
	sc := spec.Build()
	au := audit.Run(sc)
	an := plan.Analyze(sc, sc.Pools)

	a := &All{Scenario: sc, Audit: au, Analysis: an, Bundles: map[Kind]*Bundle{}}
	for _, b := range Library {
		a.Order = append(a.Order, b.Kind)
		a.Bundles[b.Kind] = buildOne(sc, au, an, b)
	}
	return a
}

func buildOne(sc *spec.Scenario, au *audit.Fleet, an *plan.Analysis, b Brief) *Bundle {
	cfg := sim.RunConfig{Name: string(b.Kind)}
	var waves []sim.Wave
	switch b.Kind {
	case KindNaive:
		cfg.Preflight = false
		cfg.Remediate = false
		waves = nil // one wave of the whole fleet
	case KindPreflight:
		cfg.Preflight = true
		waves = nil
	case KindRhythm:
		cfg.Preflight = true
		cfg.StaggerS = an.Rhythm.StaggerS
		waves = an.Rhythm.Waves
	case KindFull:
		cfg.Preflight = true
		cfg.Remediate = true
		cfg.StaggerS = an.Rhythm.StaggerS
		waves = an.Rhythm.Waves
	}
	r := sim.New(sc, au, cfg, waves).Run()
	sum := report.Build(r, b.Label, sc, true)
	if waves == nil {
		waves = []sim.Wave{{AtS: 0, Count: len(sc.Machines)}}
	}
	return &Bundle{Brief: b, Result: r, Summary: sum, Waves: waves, Config: cfg}
}

// Comparison is the side-by-side table data.
type Comparison struct {
	Rows []ComparisonRow `json:"rows"`
}

// ComparisonRow is one strategy's outcome.
type ComparisonRow struct {
	Kind      Kind          `json:"kind"`
	Label     string        `json:"label"`
	MakespanH float64       `json:"makespanH"`
	Installed int           `json:"installed"`
	Compliant int           `json:"compliant"`
	Failed    int           `json:"failed"`
	Held      int           `json:"held"`
	Capacity  int           `json:"capacityLost"`
	ExactRate float64       `json:"exactRate"`
	Triage    report.Triage `json:"triage"`
}

// Compare builds the comparison from computed bundles.
func (a *All) Compare() Comparison {
	var c Comparison
	for _, k := range a.Order {
		b := a.Bundles[k]
		s := b.Summary
		c.Rows = append(c.Rows, ComparisonRow{
			Kind: k, Label: b.Brief.Label, MakespanH: s.MakespanH,
			Installed: s.Installed, Compliant: s.Installed - s.Noncompliant,
			Failed: s.Failed, Held: s.Held, Capacity: s.Capacity,
			ExactRate: s.Score.ExactRate, Triage: s.Triage,
		})
	}
	return c
}
