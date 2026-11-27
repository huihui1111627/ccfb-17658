// Command simctl runs the provisioning simulation: it prints the preflight
// audit, the capacity analysis, the strategy comparison and the
// ground-truth scorecard, or serves the dashboard.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"provisioning-sim/internal/model"
	"provisioning-sim/internal/report"
	"provisioning-sim/internal/run"
	"provisioning-sim/internal/spec"
	"provisioning-sim/internal/webui"
)

func main() {
	var (
		addr    = flag.String("serve", "", "serve the dashboard on this address (e.g. :8080)")
		jsonOut = flag.String("json", "", "write the comparison summary as JSON to this file")
		verbose = flag.Bool("v", false, "print per-machine scorecard detail")
	)
	flag.Parse()

	if *addr != "" {
		s, err := webui.New()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if err := s.Serve(*addr); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	all := run.Compute()
	printScenario(all)
	printAudit(all)
	printCapacity(all)
	printComparison(all)
	printScorecard(all, *verbose)
	printRhythm(all)

	if *jsonOut != "" {
		if err := writeJSON(*jsonOut, all); err != nil {
			fmt.Fprintln(os.Stderr, "error writing json:", err)
		} else {
			fmt.Println("\nJSON written to", *jsonOut)
		}
	}
}

func printScenario(a *run.All) {
	sc := a.Scenario
	fmt.Println("== 场景 ==")
	fmt.Printf("  机柜 %d 个 × 每柜 %d 台 = %d 台;机型 %v\n",
		spec.CabinetCount, spec.PerCabinet, len(sc.Machines), spec.ModelOrder)
	mods := map[string]int{}
	for _, m := range sc.Machines {
		mods[m.Model]++
	}
	for _, k := range spec.ModelOrder {
		t := sc.Templates[k]
		fmt.Printf("    %-8s %3d 台  模板: %-6s 至少%d块%s盘 镜像%s(%.0fGB) 网卡固件≥%s\n",
			k, mods[k], t.RAID.Level, t.MinDiskCount, t.DiskKind,
			t.ImageName, float64(t.ImageGB), t.RequiresNICFirmware)
	}
	fmt.Printf("  共享资源: 地址池 %d / DHCP并发 %d / TFTP %d路 / 镜像 %d路 %dMB/s / 装机 %d位\n",
		sc.Pools.DHCPLeases, sc.Pools.DHCPSeats, sc.Pools.TFTPStreams,
		sc.Pools.HTTPStreams, sc.Pools.HTTPMBps, sc.Pools.InstallShare)

	truth := map[model.RootCause]int{}
	for _, m := range sc.Machines {
		truth[m.Truth]++
	}
	fmt.Println("\n  埋入问题(真值):")
	type rc struct {
		r model.RootCause
		n int
	}
	var rows []rc
	for r, n := range truth {
		if r != model.R_None {
			rows = append(rows, rc{r, n})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n })
	for _, x := range rows {
		fmt.Printf("    %4d  %s\n", x.n, model.RootCauseLabel(x.r))
	}
	fmt.Printf("    正常 %d 台\n", truth[model.R_None])
}

func printAudit(a *run.All) {
	au := a.Audit
	fmt.Println("\n== 装机前核对 ==")
	fmt.Printf("  合格 %d 台,拦截 %d 台\n", au.OK, au.HeldCount)
	if len(au.TemplateIssues) > 0 {
		fmt.Println("  模板级问题:")
		for _, ti := range au.TemplateIssues {
			fmt.Printf("    [%s] %s\n", ti.Model, ti.Detail)
		}
	} else {
		fmt.Println("  模板级问题: 无")
	}
	if len(au.SwitchIssues) > 0 {
		fmt.Println("  机柜级问题(整柜共因):")
		for _, si := range au.SwitchIssues {
			fmt.Printf("    %s: %s\n", si.Switch, si.Issue.Detail)
		}
	}
	fmt.Println("  拦截原因(按检查项):")
	for _, fc := range report.BuildAudit(au).ByField {
		fmt.Printf("    %-26s %d\n", fc.Field, fc.Count)
	}
}

func printCapacity(a *run.All) {
	an := a.Analysis
	fmt.Println("\n== 全量上电时的服务瓶颈 ==")
	for _, b := range an.AllAtOnce {
		state := "未满"
		if b.Capacity > 0 && b.PeakInUse >= b.Capacity {
			state = "已满"
		}
		fmt.Printf("  #%d %-22s 容量%4d 峰值占用%4d 排队峰值%4d 首次满@%5.0fs %s\n",
			b.Rank, b.Label, b.Capacity, b.PeakInUse, b.PeakQueue, b.FirstFullS, state)
		fmt.Printf("      %s\n", b.Coaching)
	}
}

func printComparison(a *run.All) {
	c := a.Compare()
	fmt.Println("\n== 方案对比 ==")
	fmt.Printf("  %-30s %7s %6s %6s %6s %6s %8s %8s\n",
		"方案", "用时h", "装好", "合规", "故障", "拦截", "归因准确", "排查工分")
	for _, r := range c.Rows {
		fmt.Printf("  %-30s %7.1f %6d %6d %6d %6d %7.0f%% %8.0f\n",
			r.Label, r.MakespanH, r.Installed, r.Compliant, r.Failed, r.Held,
			r.ExactRate*100, r.Triage.EffortMin)
	}
	fmt.Println("  注:排查工分=共因批处理(4分/类)+个案排查(25分/台),按现场节奏估算")
	for _, k := range a.Order {
		t := a.Bundles[k].Summary.Triage
		if k == "naive" || k == "full" {
			fmt.Printf("    %s: %d 类意外症状,%d 类共因批处理(覆盖 %d 台),%d 台需单台排查\n",
				a.Bundles[k].Brief.Label, t.UniqueSignatures, t.BatchFixes, t.BatchCovered, t.SoloCases)
		}
	}
}

func printScorecard(a *run.All, verbose bool) {
	fmt.Println("\n== 逐台核对:真值 vs 工具归因 ==")
	for _, k := range a.Order {
		b := a.Bundles[k]
		sc := b.Summary.Score
		fmt.Printf("  %s:\n", b.Brief.Label)
		fmt.Printf("    精确 %d / 近似 %d / 错判 %d / 未判定 %d / 误报 %d  (共 %d 个真问题)\n",
			sc.Exact, sc.Symptom, sc.Miss, sc.Unresolved, sc.FalsePos, sc.TotalDefect)
		fmt.Printf("    精确率 %.0f%%  召回(精确+近似) %.0f%%\n",
			sc.ExactRate*100, sc.Recall*100)
		if verbose {
			for _, p := range sc.Pairs {
				if p.Match == "exact" || p.Verdict == "ok" {
					continue
				}
				fmt.Printf("      #%-4d %-14s %-10s 真值:%-24s 工具:%s\n",
					p.ID, p.Hostname, p.Verdict, p.Truth, p.Answer)
			}
		}
	}
}

func printRhythm(a *run.All) {
	an := a.Analysis
	fmt.Println("\n== 分批上电节奏(仿真反解) ==")
	for _, l := range an.Rhythm.Basis {
		fmt.Println("  · " + l)
	}
	fmt.Printf("  每批 %d 台,批间隔 %.0f 秒,共 %d 批\n",
		an.Rhythm.PerWave, an.Rhythm.WaveGapS, len(an.Rhythm.Waves))
	fmt.Println("  分批规模扫描(每批台数 → 用时/资源性失败):")
	for _, p := range an.Probe {
		fmt.Printf("    %4d 台 × %2d 批 → %5.1f h  资源失败 %d  最大排队 %d\n",
			p.PerWave, p.Waves, p.MakespanS/3600, p.Capacity, p.MaxQueue)
	}
}

func writeJSON(path string, a *run.All) error {
	type row struct {
		Kind         string  `json:"kind"`
		Label        string  `json:"label"`
		MakespanH    float64 `json:"makespanH"`
		Installed    int     `json:"installed"`
		Failed       int     `json:"failed"`
		Held         int     `json:"held"`
		Capacity     int     `json:"capacityLost"`
		Noncompliant int     `json:"noncompliant"`
		ExactRate    float64 `json:"exactRate"`
		Recall       float64 `json:"recall"`
		FalsePos     int     `json:"falsePositives"`
		TriageMin    float64 `json:"triageMinutes"`
	}
	out := map[string]any{}
	var rows []row
	for _, k := range a.Order {
		b := a.Bundles[k]
		s := b.Summary
		rows = append(rows, row{
			Kind: string(k), Label: b.Brief.Label, MakespanH: s.MakespanH,
			Installed: s.Installed, Failed: s.Failed, Held: s.Held, Capacity: s.Capacity,
			Noncompliant: s.Noncompliant, ExactRate: s.Score.ExactRate,
			Recall: s.Score.Recall, FalsePos: s.Score.FalsePos,
			TriageMin: s.Triage.EffortMin,
		})
	}
	out["comparison"] = rows
	out["rhythm"] = a.Analysis.Rhythm
	out["bottlenecks"] = a.Analysis.AllAtOnce
	out["probe"] = a.Analysis.Probe
	out["audit"] = report.BuildAudit(a.Audit)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
