package sim

import (
	"encoding/json"
	"fmt"
	"math/rand"

	"baremetal-rollout/internal/diagnose"
	"baremetal-rollout/internal/domain"
	"baremetal-rollout/internal/plan"
)

type Mode string

const (
	ModeStorm   Mode = "storm"
	ModeBatched Mode = "batched"
)

type QueueRuntime struct {
	Name               string  `json:"name"`
	RatePerSec         float64 `json:"ratePerSec"`
	NextFreeMS         int64   `json:"-"`
	Submitted          int64   `json:"submitted"`
	Completed          int64   `json:"completed"`
	TimedOut           int64   `json:"timedOut"`
	Retries            int64   `json:"retries"`
	PeakWaitMS         int64   `json:"peakWaitMs"`
	FirstSaturatedAtMS int64   `json:"firstSaturatedAtMs"`
}

func (q *QueueRuntime) Submit(nowMS int64) int64 {
	if q.NextFreeMS < nowMS {
		q.NextFreeMS = nowMS
	}
	gapMS := int64(1000.0 / q.RatePerSec)
	if gapMS < 1 {
		gapMS = 1
	}
	q.NextFreeMS += gapMS
	wait := q.NextFreeMS - nowMS
	q.Submitted++
	if wait > q.PeakWaitMS {
		q.PeakWaitMS = wait
	}
	// >2.5s of queueing is enough to put PXE clients near retry thresholds.
	if wait > 2500 && q.FirstSaturatedAtMS == 0 {
		q.FirstSaturatedAtMS = nowMS
	}
	return q.NextFreeMS
}

type TransferMeter struct {
	Service            string  `json:"service"`
	EgressBps          float64 `json:"egressBps"`
	SafeStreams        int     `json:"safeStreams"`
	HardStreams        int     `json:"hardStreams"`
	PeakActive         int     `json:"peakActive"`
	Completed          int64   `json:"completed"`
	Timeouts           int64   `json:"timeouts"`
	BytesTransferred   int64   `json:"bytesTransferred"`
	PeakPerStreamBps   float64 `json:"peakPerStreamBps"`
	WorstPerStreamBps  float64 `json:"worstPerStreamBps"`
	FirstSaturatedAtMS int64   `json:"firstSaturatedAtMs"`
}

type Simulation struct {
	Fleet   *domain.Fleet `json:"fleet"`
	Mode    Mode          `json:"mode"`
	Profile plan.Profile  `json:"profile"`
	Plan    plan.Plan     `json:"plan"`
	TimeMS  int64         `json:"timeMs"`
	Done    bool          `json:"done"`

	RNG *rand.Rand `json:"-"`

	DHCP      *QueueRuntime             `json:"dhcp"`
	TFTPSetup *QueueRuntime             `json:"tftpSetup"`
	Transfers map[string]*TransferMeter `json:"transfers"`

	ActiveBatch   int     `json:"activeBatch"`
	BatchStartMS  []int64 `json:"batchStartMs"`
	BatchDrainMS  []int64 `json:"batchDrainMs"`
	BatchFinishMS []int64 `json:"batchFinishMs"`
}

func CloneFleet(f *domain.Fleet) (*domain.Fleet, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var out domain.Fleet
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	// JSON uses pointers for machines; ensure non-nil maps after clone.
	for _, m := range out.Machines {
		if m.Attempts == nil {
			m.Attempts = map[string]int{}
		}
	}
	return &out, nil
}

func New(base *domain.Fleet, mode Mode, profile plan.Profile, seed int64) (*Simulation, error) {
	f, err := CloneFleet(base)
	if err != nil {
		return nil, err
	}
	p := plan.Build(f, profile)
	s := &Simulation{
		Fleet: f, Mode: mode, Profile: profile, Plan: p,
		RNG:       rand.New(rand.NewSource(seed + int64(len(mode))*7919)),
		DHCP:      &QueueRuntime{Name: "dhcp-ipam", RatePerSec: profile.DHCPRatePerSec},
		TFTPSetup: &QueueRuntime{Name: "tftp-setup", RatePerSec: profile.TFTPSetupRatePerSec},
		Transfers: map[string]*TransferMeter{
			"tftp":  {Service: "tftp", EgressBps: float64(profile.TFTPMaxSessions) * profile.TFTPSessionBps, SafeStreams: int(float64(profile.TFTPMaxSessions) * profile.TargetUtil), HardStreams: profile.TFTPMaxSessions},
			"http":  {Service: "http", EgressBps: profile.HTTPEgressBps, SafeStreams: int(profile.HTTPEgressBps / profile.HTTPMinStreamBps * profile.TargetUtil), HardStreams: int(profile.HTTPEgressBps/profile.HTTPMinStreamBps) + 64},
			"image": {Service: "image", EgressBps: profile.ImageEgressBps, SafeStreams: profile.ImageSafeStreams, HardStreams: int(profile.ImageEgressBps / profile.ImageMinStreamBps)},
		},
		BatchStartMS:  make([]int64, len(p.Batches)+1),
		BatchDrainMS:  make([]int64, len(p.Batches)+1),
		BatchFinishMS: make([]int64, len(p.Batches)+1),
	}
	if s.Transfers["image"].HardStreams < profile.ImageSafeStreams {
		s.Transfers["image"].HardStreams = profile.ImageSafeStreams
	}

	if mode == ModeStorm {
		for _, m := range f.Machines {
			m.PowerAtMS = 3_000
		}
	}
	// Batched power-on is scheduled dynamically by powerScheduler.
	return s, nil
}

func (s *Simulation) Advance(dtMS int64) {
	if s.Done || dtMS <= 0 {
		return
	}
	// External callers may request a large UI time jump; keep the state machine
	// on one-second internal ticks so stage timings do not depend on play speed.
	const tickMS int64 = 1000
	remaining := dtMS
	for remaining > 0 && !s.Done {
		dt := tickMS
		if remaining < tickMS {
			dt = remaining
		}
		remaining -= dt
		s.advanceTick(dt)
	}
}

func (s *Simulation) advanceTick(dtMS int64) {
	s.TimeMS += dtMS
	s.powerScheduler()

	for _, m := range s.Fleet.Machines {
		if m.Status == domain.StatusPending {
			if m.PowerAtMS > 0 && s.TimeMS >= m.PowerAtMS {
				s.transition(m, domain.StagePowerOn)
			}
			continue
		}
		if isTerminal(m.Status) {
			continue
		}
		s.stepMachine(m)
	}
	s.stepTransfers(dtMS)
	s.checkDone()
}

func (s *Simulation) Run(maxMS int64, tickMS int64) {
	if tickMS <= 0 {
		tickMS = 1000
	}
	for !s.Done && s.TimeMS < maxMS {
		dt := tickMS
		if s.TimeMS+dt > maxMS {
			dt = maxMS - s.TimeMS
		}
		s.Advance(dt)
	}
	if s.TimeMS >= maxMS && !s.Done {
		// Leave active machines visible as incomplete; no fake terminal state.
		s.Done = true
	}
}

func isTerminal(st domain.Status) bool {
	return st == domain.StatusSuccess || st == domain.StatusBlocked || st == domain.StatusFailed
}

func (s *Simulation) checkDone() {
	for _, m := range s.Fleet.Machines {
		if !isTerminal(m.Status) {
			return
		}
	}
	s.Done = true
}

func (s *Simulation) powerScheduler() {
	if s.Mode != ModeBatched {
		return
	}
	if s.ActiveBatch == 0 {
		s.startBatch(1, s.TimeMS)
	}

	// Record terminal barriers independently after the pipeline has moved on.
	for n := 1; n <= len(s.Plan.Batches); n++ {
		if s.BatchFinishMS[n] == 0 && s.batchTerminal(n) {
			s.BatchFinishMS[n] = s.TimeMS
		}
	}

	if s.ActiveBatch < 1 || s.ActiveBatch > len(s.Plan.Batches) {
		return
	}
	n := s.ActiveBatch
	if s.BatchDrainMS[n] == 0 && s.batchImageDrained(n) {
		s.BatchDrainMS[n] = s.TimeMS
		if n < len(s.Plan.Batches) {
			// Image bytes have left the shared registry; overlap local
			// install/reboot with the next batch's PXE/DHCP/boot transfer.
			s.startBatch(n+1, s.TimeMS+5_000)
		}
	}
}

func (s *Simulation) startBatch(n int, atMS int64) {
	if n < 1 || n > len(s.Plan.Batches) || s.ActiveBatch == n {
		return
	}
	s.ActiveBatch = n
	s.BatchStartMS[n] = atMS
	b := s.Plan.Batches[n-1]
	for pos, id := range b.MachineIDs {
		m := s.Fleet.MachineByID(id)
		if m == nil {
			continue
		}
		offset := int64(float64(pos) / s.Profile.PowerRatePerSec * 1000)
		m.PowerAtMS = atMS + offset
	}
}

func (s *Simulation) batchTerminal(n int) bool {
	for _, m := range s.Fleet.Machines {
		if m.Batch == n && !isTerminal(m.Status) {
			return false
		}
	}
	return true
}

// batchImageDrained reports that every machine in a batch has either reached a
// terminal pre-image failure or finished shared-network image download.
func (s *Simulation) batchImageDrained(n int) bool {
	installIndex := domain.StageOrderIndex(domain.StageInstallOS)
	for _, m := range s.Fleet.Machines {
		if m.Batch != n {
			continue
		}
		if isTerminal(m.Status) {
			continue
		}
		if domain.StageOrderIndex(m.Stage) < installIndex {
			return false
		}
	}
	return true
}

func (s *Simulation) transition(m *domain.Machine, stage domain.Stage) {
	m.Stage = stage
	m.StageAtMS = s.TimeMS
	m.UpdatedAtMS = s.TimeMS
	m.NextCheckAtMS = 0
	m.TimeoutAtMS = 0
	m.NextStage = ""
	m.PendingAction = ""
	if stage != domain.StageDHCP && stage != domain.StageTFTPBoot && stage != domain.StageHTTPBoot && stage != domain.StageImageDownload {
		m.Xfer = nil
	}
	s.enterStage(m, stage)
}

func (s *Simulation) schedule(m *domain.Machine, delayMS int64, next domain.Stage) {
	m.Xfer = nil
	m.PendingAction = ""
	m.TimeoutAtMS = 0
	m.NextCheckAtMS = s.TimeMS + delayMS
	m.NextStage = next
}

func (s *Simulation) scheduleAction(m *domain.Machine, delayMS int64, action string) {
	m.NextStage = ""
	m.TimeoutAtMS = 0
	m.NextCheckAtMS = s.TimeMS + delayMS
	m.PendingAction = action
}

func (s *Simulation) log(m *domain.Machine, stage domain.Stage, proto, dir, msg string, ok bool, latencyMS int64) {
	m.Logs = append(m.Logs, domain.LogEntry{
		TimeMS: s.TimeMS, Stage: stage, Protocol: proto, Direction: dir,
		Endpoint: "", Message: msg, OK: ok, LatencyMS: latencyMS,
	})
}

func (s *Simulation) logEP(m *domain.Machine, stage domain.Stage, proto, dir, endpoint, msg string, ok bool, latencyMS int64) {
	m.Logs = append(m.Logs, domain.LogEntry{
		TimeMS: s.TimeMS, Stage: stage, Protocol: proto, Direction: dir,
		Endpoint: endpoint, Message: msg, OK: ok, LatencyMS: latencyMS,
	})
}

func (s *Simulation) randomBetween(minMS, maxMS int64) int64 {
	if maxMS <= minMS {
		return minMS
	}
	return minMS + s.RNG.Int63n(maxMS-minMS+1)
}

func (s *Simulation) blockWithIssue(m *domain.Machine, issue domain.Issue, scope string, related string) {
	attr := diagnose.AttributionForIssue(issue, scope)
	if related != "" {
		attr.RelatedID = related
	}
	m.Attribution = &attr
	m.Status = domain.StatusBlocked
	m.FinishedAtMS = s.TimeMS
	m.UpdatedAtMS = s.TimeMS
	m.NextCheckAtMS = 0
	m.TimeoutAtMS = 0
	m.Xfer = nil
}

func (s *Simulation) failOverload(m *domain.Machine, code string, service string, detail string) {
	issue := domain.Issue{Code: code, Category: "service", Message: detail, Evidence: map[string]string{"service": service}}
	attr := diagnose.AttributionForIssue(issue, "service")
	attr.Confidence = 0.91
	attr.RelatedID = service
	m.Attribution = &attr
	m.Status = domain.StatusFailed
	m.LastError = detail
	m.FinishedAtMS = s.TimeMS
	m.UpdatedAtMS = s.TimeMS
	m.NextCheckAtMS = 0
	m.TimeoutAtMS = 0
	m.Xfer = nil
	s.logEP(m, m.Stage, "SCHED", "!", service, detail, false, 0)
}

func (s *Simulation) succeed(m *domain.Machine) {
	m.Status = domain.StatusSuccess
	m.Stage = domain.StageDone
	m.Progress = 1
	m.FinishedAtMS = s.TimeMS
	m.UpdatedAtMS = s.TimeMS
	m.NextCheckAtMS = 0
	m.TimeoutAtMS = 0
	m.Xfer = nil
	m.Attribution = nil
	s.logEP(m, domain.StageDone, "AGENT", "↔", "provisioning-controller", "health check passed；节点已加入生产资源池", true, 0)
}

func terminalReportLine(m *domain.Machine) string {
	if m.Attribution != nil {
		return fmt.Sprintf("%s: %s", m.Attribution.Code, m.Attribution.RootCause)
	}
	return string(m.Status)
}
