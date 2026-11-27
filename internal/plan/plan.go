// Package plan finds where the shared services bind first and derives a
// power-on rhythm by probing the simulator, not by closed-form arithmetic.
package plan

import (
	"fmt"
	"math"
	"sort"

	"provisioning-sim/internal/model"
	"provisioning-sim/internal/sim"
	"provisioning-sim/internal/spec"
)

// Binding is one shared resource's verdict under a given batch size.
type Binding struct {
	Name        string  `json:"name"`
	Label       string  `json:"label"`
	Capacity    int     `json:"capacity"`
	PeakInUse   int     `json:"peakInUse"`
	PeakQueue   int     `json:"peakQueue"`
	FirstFullS  float64 `json:"firstFullS"`
	Served      int     `json:"served"`
	Lost        int     `json:"lost"`
	BusySeconds float64 `json:"busySeconds"`
	// Coaching is the quantitative advice for this resource.
	Coaching string `json:"coaching"`
	Rank     int    `json:"rank"`
}

// Rhythm is the recommended power-on plan.
type Rhythm struct {
	Waves        []sim.Wave `json:"waves"`
	PerWave      int        `json:"perWave"`
	WaveGapS     float64    `json:"waveGapS"`
	StaggerS     float64    `json:"staggerS"`
	EstMakespanS float64    `json:"estMakespanS"`
	EstFailed    int        `json:"estFailed"`
	Bottleneck   string     `json:"bottleneck"`
	Basis        []string   `json:"basis"`
}

// Analysis is the full pre-run report.
type Analysis struct {
	// AllAtOnce describes what happens with today's practice.
	AllAtOnce []Binding `json:"allAtOnce"`
	Rhythm    Rhythm    `json:"rhythm"`
	// Probe is the per-wave-size sweep used to derive the rhythm.
	Probe []WaveProbe `json:"probe"`
}

// WaveProbe is one measurement point of the planner's search.
type WaveProbe struct {
	PerWave   int     `json:"perWave"`
	Waves     int     `json:"waves"`
	MakespanS float64 `json:"makespanS"`
	Failed    int     `json:"failed"`
	Capacity  int     `json:"capacityLost"`
	MaxQueue  int     `json:"maxQueue"`
}

// ServiceLabels names each pool for the UI.
var ServiceLabels = map[string]string{
	"dhcp-leases": "地址池(DHCP 租约)",
	"dhcp-seats":  "DHCP 并发处理",
	"tftp":        "引导文件服务(TFTP)",
	"http":        "安装镜像服务(HTTP)",
	"install":     "装机器并发",
}

// Analyze probes the simulator: one all-at-once run to show today's wall,
// then a sweep of wave sizes to find the largest one that sustains no
// contention loss.
func Analyze(sc *spec.Scenario, pools model.Pools) *Analysis {
	a := &Analysis{}

	// Today's practice: one wave of the whole fleet, no preflight.
	base := sim.New(sc, nil, sim.RunConfig{Name: "all-at-once", Pools: &pools}, nil).Run()
	a.AllAtOnce = rankBindings(base, spec.Total)

	// Sweep: try progressively smaller waves with a gap equal to the image
	// service's per-stream transfer time. A wave of N with gap G is the same
	// rhythm the floor would follow manually.
	target := a.AllAtOnce
	if len(target) > 0 {
		// Headline bottleneck for the report; the sweep decides the size.
	}
	for _, n := range []int{200, 160, 128, 96, 80, 64, 48, 32, 24, 16, 12, 8} {
		waves := buildWaves(n, estimateGap(n, pools))
		r := sim.New(sc, nil, sim.RunConfig{Name: fmt.Sprintf("w%d", n), Pools: &pools, StaggerS: 0.5}, waves).Run()
		capLoss := 0
		for _, m := range r.Machines {
			if m.Verdict == "capacity" {
				capLoss++
			}
		}
		a.Probe = append(a.Probe, WaveProbe{
			PerWave: n, Waves: len(waves), MakespanS: r.MakespanS,
			Failed: r.Failed, Capacity: capLoss, MaxQueue: maxQueue(r),
		})
	}

	// The recommended size is the largest probing wave with zero
	// capacity-induced loss. Draw-downs to smaller waves only trade time.
	best, bestT := 0, math.Inf(1)
	for _, p := range a.Probe {
		if p.Capacity == 0 {
			if p.MakespanS < bestT || (p.MakespanS == bestT && p.PerWave > best) {
				best, bestT = p.PerWave, p.MakespanS
			}
		}
	}
	if best == 0 && len(a.Probe) > 0 {
		best = a.Probe[len(a.Probe)-1].PerWave
	}
	gap := estimateGap(best, pools)
	waves := buildWaves(best, gap)
	est := sim.New(sc, nil, sim.RunConfig{Name: "est", Pools: &pools, StaggerS: 0.5}, waves).Run()
	capLoss := 0
	for _, m := range est.Machines {
		if m.Verdict == "capacity" {
			capLoss++
		}
	}

	bottleneck := "镜像服务"
	if len(a.AllAtOnce) > 0 {
		bottleneck = a.AllAtOnce[0].Label
	}
	a.Rhythm = Rhythm{
		Waves: waves, PerWave: best, WaveGapS: gap, StaggerS: 0.5,
		EstMakespanS: est.MakespanS, EstFailed: capLoss, Bottleneck: bottleneck,
		Basis: []string{
			fmt.Sprintf("整批 %d 台上电时 %s 最先满,排队 %d", spec.Total, bottleneck, a.AllAtOnce[0].PeakQueue),
			fmt.Sprintf("按每批 %d 台、间隔 %.0f 秒分批,仿真无因资源不足而产生的失败", best, gap),
			fmt.Sprintf("共 %d 批,预计 %.1f 小时装完,资源性失败 %d 台", len(waves), est.MakespanS/3600, capLoss),
		},
	}
	return a
}

func rankBindings(r *sim.Result, demand int) []Binding {
	var out []Binding
	for name, label := range ServiceLabels {
		pk := r.Peak[name]
		if pk == nil {
			continue
		}
		b := Binding{
			Name: name, Label: label, Capacity: pk.Capacity, PeakInUse: pk.PeakInUse,
			PeakQueue: pk.PeakQueue, FirstFullS: pk.FirstFullS, Served: pk.Served, Lost: pk.Lost,
			BusySeconds: pk.BusySecond,
		}
		switch {
		case pk.Capacity == 0:
			b.Coaching = "未配置"
		case name == "dhcp-leases":
			b.Coaching = fmt.Sprintf("地址池 %d 个,峰值占用 %d/%d;需容纳同批上电的主机数",
				pk.Capacity, pk.PeakInUse, pk.Capacity)
		case name == "http":
			b.Coaching = fmt.Sprintf("镜像流 %d 路已满,排队峰值 %d;扩容到 %d 路或降低单流速率",
				pk.Capacity, pk.PeakQueue, minCap(pk.Capacity+pk.PeakQueue, demand))
		case name == "tftp":
			b.Coaching = fmt.Sprintf("引导流 %d 路已满,排队峰值 %d;每路约 %.0f 秒,扩容到 %d 路",
				pk.Capacity, pk.PeakQueue, 6.0, minCap(pk.Capacity+pk.PeakQueue, demand))
		case name == "install":
			b.Coaching = fmt.Sprintf("装机位 %d 个,峰值占用 %d;不作为首要约束", pk.Capacity, pk.PeakInUse)
		default:
			b.Coaching = fmt.Sprintf("并发 %d,峰值占用 %d", pk.Capacity, pk.PeakInUse)
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeakQueue != out[j].PeakQueue {
			return out[i].PeakQueue > out[j].PeakQueue
		}
		return out[i].PeakInUse > out[j].PeakInUse
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// estimateGap is the interval a manual batch rhythm would use: long enough
// for the previous batch's images to finish downloading at the service's
// per-stream rate, which is the duration that makes the queue drain.
func estimateGap(n int, p model.Pools) float64 {
	mbps := float64(p.HTTPMBps) / float64(p.HTTPStreams)
	if mbps > float64(p.HTTPStreamMBps) {
		mbps = float64(p.HTTPStreamMBps)
	}
	// Median image size across the fleet templates.
	img := 9.5
	dur := img * 1024 / mbps
	// The batch needs about ceil(n/streams) turnarounds.
	turn := math.Ceil(float64(n) / float64(p.HTTPStreams))
	gap := dur * math.Max(1, turn)
	if gap < 120 {
		gap = 120
	}
	if gap > 3600 {
		gap = 3600
	}
	return gap
}

func buildWaves(perWave int, gap float64) []sim.Wave {
	var out []sim.Wave
	left := spec.Total
	at := 0.0
	for left > 0 {
		c := perWave
		if left < c {
			c = left
		}
		out = append(out, sim.Wave{AtS: at, Count: c})
		left -= c
		at += gap
	}
	return out
}

func maxQueue(r *sim.Result) int {
	q := 0
	for _, pk := range r.Peak {
		if pk.PeakQueue > q {
			q = pk.PeakQueue
		}
	}
	return q
}

func minCap(a, b int) int {
	if a < b {
		return a
	}
	return b
}
