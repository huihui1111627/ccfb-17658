package report

import (
	"fmt"

	"baremetal-rollout/internal/diagnose"
	"baremetal-rollout/internal/domain"
	"baremetal-rollout/internal/plan"
	"baremetal-rollout/internal/sim"
)

type BottleneckObservation struct {
	Stage       domain.Stage `json:"stage"`
	Service     string       `json:"service"`
	PeakLoad    float64      `json:"peakLoad"`
	SafeLoad    float64      `json:"safeLoad"`
	Utilization float64      `json:"utilization"`
	PeakWaitMS  int64        `json:"peakWaitMs,omitempty"`
	Timeouts    int64        `json:"timeouts"`
	Completed   int64        `json:"completed"`
	FirstAtMS   int64        `json:"firstAtMs"`
	Severity    string       `json:"severity"`
	Detail      string       `json:"detail"`
}

type RunReport struct {
	Mode          string                        `json:"mode"`
	Done          bool                          `json:"done"`
	DurationMS    int64                         `json:"durationMs"`
	Total         int                           `json:"total"`
	Success       int                           `json:"success"`
	Blocked       int                           `json:"blocked"`
	Failed        int                           `json:"failed"`
	Incomplete    int                           `json:"incomplete"`
	Unsuccessful  int                           `json:"unsuccessful"`
	StageCounts   map[domain.Stage]int          `json:"stageCounts"`
	StatusCounts  map[domain.Status]int         `json:"statusCounts"`
	ReasonCounts  map[string]int                `json:"reasonCounts"`
	ReasonLabels  map[string]string             `json:"reasonLabels"`
	DHCP          *sim.QueueRuntime             `json:"dhcp"`
	TFTPSetup     *sim.QueueRuntime             `json:"tftpSetup"`
	Transfers     map[string]*sim.TransferMeter `json:"transfers"`
	Bottlenecks   []BottleneckObservation       `json:"bottlenecks"`
	Clusters      []diagnose.RackCluster        `json:"clusters"`
	BatchStartMS  []int64                       `json:"batchStartMs"`
	BatchDrainMS  []int64                       `json:"batchDrainMs"`
	BatchFinishMS []int64                       `json:"batchFinishMs"`
}

type Comparison struct {
	Total              int                     `json:"total"`
	Seed               int64                   `json:"seed"`
	Plan               plan.Plan               `json:"plan"`
	Storm              RunReport               `json:"storm"`
	Batched            RunReport               `json:"batched"`
	BatchAccuracy      diagnose.AccuracyReport `json:"batchAccuracy"`
	StormAccuracy      diagnose.AccuracyReport `json:"stormAccuracy"`
	TimeSavingMS       int64                   `json:"timeSavingMs"`
	StormExtraFailures int                     `json:"stormExtraFailures"`
	SuccessGain        int                     `json:"successGain"`
}

const DefaultHorizonMS = 6 * 60 * 60 * 1000

func RunScenario(base *domain.Fleet, mode sim.Mode, seed int64) (*sim.Simulation, RunReport, error) {
	profile := plan.DefaultProfile()
	s, err := sim.New(base, mode, profile, seed)
	if err != nil {
		return nil, RunReport{}, err
	}
	s.Run(DefaultHorizonMS, 1000)
	clusters := diagnose.ClusterRackIssues(s.Fleet)
	rep := Summarize(s, clusters)
	return s, rep, nil
}

func RunComparison(base *domain.Fleet, seed int64) (Comparison, error) {
	stormSim, stormReport, err := RunScenario(base, sim.ModeStorm, seed)
	if err != nil {
		return Comparison{}, err
	}
	batchSim, batchReport, err := RunScenario(base, sim.ModeBatched, seed)
	if err != nil {
		return Comparison{}, err
	}
	stormAccuracy := diagnose.Accuracy(stormSim.Fleet.Machines)
	batchAccuracy := diagnose.Accuracy(batchSim.Fleet.Machines)

	comp := Comparison{
		Total: base.Total, Seed: seed, Plan: batchSim.Plan,
		Storm: stormReport, Batched: batchReport,
		BatchAccuracy: batchAccuracy, StormAccuracy: stormAccuracy,
		TimeSavingMS:       stormReport.DurationMS - batchReport.DurationMS,
		StormExtraFailures: stormReport.Unsuccessful - batchReport.Unsuccessful,
		SuccessGain:        batchReport.Success - stormReport.Success,
	}
	return comp, nil
}

func Summarize(s *sim.Simulation, clusters []diagnose.RackCluster) RunReport {
	r := RunReport{
		Mode: string(s.Mode), Done: s.Done, DurationMS: s.TimeMS,
		Total:       len(s.Fleet.Machines),
		StageCounts: map[domain.Stage]int{}, StatusCounts: map[domain.Status]int{},
		ReasonCounts: map[string]int{}, ReasonLabels: domain.CodeLabels,
		DHCP: s.DHCP, TFTPSetup: s.TFTPSetup, Transfers: s.Transfers,
		Clusters: clusters, BatchStartMS: s.BatchStartMS, BatchDrainMS: s.BatchDrainMS, BatchFinishMS: s.BatchFinishMS,
	}
	for _, m := range s.Fleet.Machines {
		r.StageCounts[m.Stage]++
		r.StatusCounts[m.Status]++
		switch m.Status {
		case domain.StatusSuccess:
			r.Success++
			r.ReasonCounts[domain.CodeHealthy]++
		case domain.StatusBlocked:
			r.Blocked++
			if m.Attribution != nil {
				r.ReasonCounts[m.Attribution.Code]++
			} else {
				r.ReasonCounts[domain.CodeUnknown]++
			}
		case domain.StatusFailed:
			r.Failed++
			if m.Attribution != nil {
				r.ReasonCounts[m.Attribution.Code]++
			} else {
				r.ReasonCounts[domain.CodeUnknown]++
			}
		default:
			r.Incomplete++
			r.ReasonCounts["IN_PROGRESS"]++
		}
	}
	r.Unsuccessful = r.Blocked + r.Failed
	r.Bottlenecks = observeBottlenecks(s)
	return r
}

func observeBottlenecks(s *sim.Simulation) []BottleneckObservation {
	out := []BottleneckObservation{
		{
			Stage: domain.StageDHCP, Service: s.DHCP.Name,
			PeakLoad: float64(s.DHCP.Submitted), SafeLoad: s.Profile.DHCPRatePerSec * s.Profile.TargetUtil,
			Utilization: queueUtil(s.DHCP), PeakWaitMS: s.DHCP.PeakWaitMS,
			Timeouts: s.DHCP.TimedOut, Completed: s.DHCP.Completed,
			FirstAtMS: s.DHCP.FirstSaturatedAtMS,
			Severity:  obsSeverity(s.DHCP.FirstSaturatedAtMS, s.DHCP.TimedOut),
			Detail:    fmt.Sprintf("提交 %d 次，完成 %d，超时 %d，峰值排队 %dms", s.DHCP.Submitted, s.DHCP.Completed, s.DHCP.TimedOut, s.DHCP.PeakWaitMS),
		},
		{
			Stage: domain.StageTFTPBoot, Service: s.TFTPSetup.Name,
			PeakLoad: float64(s.Transfers["tftp"].PeakActive), SafeLoad: float64(s.Transfers["tftp"].SafeStreams),
			Utilization: float64(s.Transfers["tftp"].PeakActive) / float64(s.Transfers["tftp"].SafeStreams),
			Timeouts:    s.Transfers["tftp"].Timeouts, Completed: s.Transfers["tftp"].Completed,
			FirstAtMS: firstNonZero(s.TFTPSetup.FirstSaturatedAtMS, s.Transfers["tftp"].FirstSaturatedAtMS),
			Severity:  obsSeverity(firstNonZero(s.TFTPSetup.FirstSaturatedAtMS, s.Transfers["tftp"].FirstSaturatedAtMS), s.Transfers["tftp"].Timeouts),
			Detail:    fmt.Sprintf("峰值 TFTP 会话 %d，安全水位 %d，超时 %d，最差单流 %.2f MB/s", s.Transfers["tftp"].PeakActive, s.Transfers["tftp"].SafeStreams, s.Transfers["tftp"].Timeouts, s.Transfers["tftp"].WorstPerStreamBps/1e6),
		},
	}
	for _, key := range []string{"http", "image"} {
		meter := s.Transfers[key]
		stage := domain.StageHTTPBoot
		if key == "image" {
			stage = domain.StageImageDownload
		}
		out = append(out, BottleneckObservation{
			Stage: stage, Service: key, PeakLoad: float64(meter.PeakActive),
			SafeLoad:    float64(meter.SafeStreams),
			Utilization: float64(meter.PeakActive) / float64(maxInt(meter.SafeStreams, 1)),
			Timeouts:    meter.Timeouts, Completed: meter.Completed, FirstAtMS: meter.FirstSaturatedAtMS,
			Severity: obsSeverity(meter.FirstSaturatedAtMS, meter.Timeouts),
			Detail:   fmt.Sprintf("峰值并发 %d，安全并发 %d，超时 %d，最差单流 %.2f MB/s", meter.PeakActive, meter.SafeStreams, meter.Timeouts, meter.WorstPerStreamBps/1e6),
		})
	}
	return out
}

func queueUtil(q *sim.QueueRuntime) float64 {
	if q.PeakWaitMS <= 0 {
		return 0.2
	}
	// A 1s queue backlog already means near line-rate utilization in this
	// aggregate queue model.
	u := 0.7 + float64(q.PeakWaitMS)/1000.0/q.RatePerSec*10
	if u > 2 {
		return 2
	}
	return u
}

func obsSeverity(firstAt int64, timeouts int64) string {
	if timeouts > 0 {
		return "critical"
	}
	if firstAt > 0 {
		return "watch"
	}
	return "ok"
}

func firstNonZero(a, b int64) int64 {
	if a != 0 {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
