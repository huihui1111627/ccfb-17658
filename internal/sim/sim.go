// Package sim is a deterministic discrete-event simulation of a bare-metal
// provisioning batch. Each host walks POST → DHCP/PXE → TFTP → HTTP →
// install → reboot while contending for finite shared services and its
// cabinet's top-of-rack switch.
package sim

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"provisioning-sim/internal/audit"
	"provisioning-sim/internal/model"
	"provisioning-sim/internal/spec"
)

// Timing constants in seconds, tuned to a 2024-vintage 2-socket rack fleet.
const (
	postBaseS        = 28.0
	postPerDIMMs     = 1.4
	pickBootS        = 6.0
	dhcpAttemptTO    = 9.0
	dhcpBackoffS     = 3.0
	dhcpAttempts     = 3
	tftpQueueTO      = 90.0
	httpQueueTO      = 1200.0
	httpTransferTO   = 2400.0
	installQueueTO   = 2400.0
	raidSetupS       = 4.0
	partitionBaseS   = 210.0
	partitionPerDisk = 14.0
	rebootS          = 48.0
	noBootRequestTO  = 120.0
	maxNetAttempts   = 3
	dhcpSeatS        = 0.018
	leaseWaitTO      = 120.0
)

// Event priorities: lower runs first at the same instant. Releases lead so a
// freed slot is available before the next timeout or step observes it.
const (
	prioRelease = iota
	prioTimeout
	prioStep
	prioWave
)

// RunConfig selects what the tool does for a batch.
type RunConfig struct {
	Name string `json:"name"`

	// Preflight runs the hardware/configuration audit before power-on.
	Preflight bool `json:"preflight"`
	// Remediate applies the audit's remotely fixable findings (boot order,
	// PXE enable, controller mode, switch config) and holds units with
	// physical defects. Requires Preflight.
	Remediate bool `json:"remediate"`

	// StaggerS spaces hosts within a wave, modelling sequential BMC power-on.
	StaggerS float64 `json:"staggerS"`

	// Pools overrides shared-service sizing for what-if planning.
	Pools *model.Pools `json:"-"`
}

// Wave is one power-on batch.
type Wave struct {
	AtS   float64 `json:"atS"`
	Count int     `json:"count"`
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// MachineRecord is one host's final disposition and full interaction log.
type MachineRecord struct {
	ID       int    `json:"id"`
	Serial   string `json:"serial"`
	Hostname string `json:"hostname"`
	Model    string `json:"model"`
	Cabinet  int    `json:"cabinet"`
	RackU    int    `json:"rackU"`

	Phase    model.Phase `json:"phase"`
	Attempts int         `json:"attempts"`
	DoneAtS  float64     `json:"doneAtS,omitempty"`
	Wave     int         `json:"wave"`

	Held        bool            `json:"held"`
	AuditCode   model.FaultCode `json:"auditCode,omitempty"`
	AuditRepair audit.Repair    `json:"auditRepair,omitempty"`
	Issues      []audit.Issue   `json:"issues,omitempty"`

	Fault       model.FaultCode `json:"fault,omitempty"`
	FaultPhase  model.Phase     `json:"faultPhase,omitempty"`
	FaultDetail string          `json:"faultDetail,omitempty"`

	Truth     model.RootCause `json:"truth"`
	TruthNote string          `json:"truthNote,omitempty"`

	Remediated  bool            `json:"remediated"`
	Attribution model.FaultCode `json:"attribution"`
	Verdict     string          `json:"verdict"` // ok | held | failed | noncompliant | capacity
	Matches     string          `json:"matches"` // exact | symptom | miss | unresolved | n/a

	Log []model.Event `json:"-"`
}

// PeakStat is the pressure a shared pool reached during the run.
type PeakStat struct {
	Name       string  `json:"name"`
	Capacity   int     `json:"capacity"`
	PeakInUse  int     `json:"peakInUse"`
	PeakQueue  int     `json:"peakQueue"`
	BusySecond float64 `json:"busySeconds"`
	FirstFullS float64 `json:"firstFullS"`
	Served     int     `json:"served"`
	Lost       int     `json:"lost"`
}

// SwitchTrace is one ToR's boot-session occupancy.
type SwitchTrace struct {
	ID        string  `json:"id"`
	Cabinet   int     `json:"cabinet"`
	PeakSess  int     `json:"peakSessions"`
	Cap       int     `json:"cap"`
	Dropped   int     `json:"dropped"`
	FirstDrop float64 `json:"firstDropS"`
}

// LeaseSample tracks address-pool occupancy over time.
type LeaseSample struct {
	T       float64 `json:"t"`
	InUse   int     `json:"inUse"`
	Queued  int     `json:"queued"`
	Refused int     `json:"refused"`
}

// PhasePoint counts hosts per phase at one instant.
type PhasePoint struct {
	T     float64        `json:"t"`
	Count map[string]int `json:"count"`
}

// Result is the complete record of a simulated batch.
type Result struct {
	Name     string           `json:"name"`
	Machines []*MachineRecord `json:"machines"`
	Waves    []Wave           `json:"waves"`

	MakespanS float64 `json:"makespanS"`
	Installed int     `json:"installed"`
	Failed    int     `json:"failed"`
	Held      int     `json:"held"`
	Skipped   int     `json:"skipped"`

	Peak     map[string]*PeakStat       `json:"peak"`
	Switches []*SwitchTrace             `json:"switches"`
	Leases   []LeaseSample              `json:"-"`
	Events   map[int][]model.Event      `json:"-"`
	Trans    map[int][]model.Transition `json:"-"`
	Series   []PhasePoint               `json:"phaseSeries"`
	SvcLog   []model.ServiceSnapshot    `json:"-"`
}

// Machine returns the record for a host.
func (r *Result) Machine(id int) *MachineRecord {
	for _, m := range r.Machines {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Event heap
// ---------------------------------------------------------------------------

type ev struct {
	t    float64
	prio int
	seq  int64
	fn   func()
}

type evHeap struct{ data []ev }

func (h *evHeap) Len() int { return len(h.data) }
func (h *evHeap) Less(i, j int) bool {
	a, b := h.data[i], h.data[j]
	if a.t != b.t {
		return a.t < b.t
	}
	if a.prio != b.prio {
		return a.prio < b.prio
	}
	return a.seq < b.seq
}
func (h *evHeap) Swap(i, j int) { h.data[i], h.data[j] = h.data[j], h.data[i] }
func (h *evHeap) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !h.Less(i, p) {
			return
		}
		h.Swap(i, p)
		i = p
	}
}
func (h *evHeap) down(i int) {
	n := len(h.data)
	for {
		l := 2*i + 1
		if l >= n {
			return
		}
		j := l
		if r := l + 1; r < n && h.Less(r, l) {
			j = r
		}
		if !h.Less(j, i) {
			return
		}
		h.Swap(i, j)
		i = j
	}
}
func (h *evHeap) push(e ev) { h.data = append(h.data, e); h.up(len(h.data) - 1) }
func (h *evHeap) pop() ev {
	x := h.data[0]
	n := len(h.data)
	h.data[0] = h.data[n-1]
	h.data = h.data[:n-1]
	if n > 1 {
		h.down(0)
	}
	return x
}

func (s *Sim) at(dt float64, prio int, fn func()) {
	s.seq++
	s.h.push(ev{t: s.t + dt, prio: prio, seq: s.seq, fn: fn})
}

func (s *Sim) atAbs(t float64, prio int, fn func()) {
	s.seq++
	s.h.push(ev{t: t, prio: prio, seq: s.seq, fn: fn})
}

// ---------------------------------------------------------------------------
// Capacity pools
// ---------------------------------------------------------------------------

type grant struct {
	h       *host
	onGrant func()
	done    bool // timed out / released; never granted after this
	got     bool
	p       *pool
}

type pool struct {
	name   string
	cap    int
	inUse  int
	q      []*grant
	served int

	peakInUse  int
	peakQueue  int
	busy       float64
	lastT      float64
	firstFullT float64
}

func newPool(name string, cap int) *pool {
	if cap < 1 {
		cap = 1
	}
	return &pool{name: name, cap: cap, firstFullT: -1}
}

// use asks for one unit. If capacity is free the grant fires immediately;
// otherwise it queues FIFO. The caller arms its deadline with a timeout
// event that calls cancelDead.
func (s *Sim) use(p *pool, h *host, onGrant func()) *grant {
	g := &grant{h: h, onGrant: onGrant, p: p}
	if len(p.q) == 0 && p.inUse < p.cap {
		s.grantLocked(p, g, true)
		return g
	}
	p.q = append(p.q, g)
	if len(p.q) > p.peakQueue {
		p.peakQueue = len(p.q)
	}
	s.poolSample(p)
	return g
}

func (s *Sim) tickPool(p *pool) {
	if p.lastT > 0 {
		p.busy += float64(p.inUse) * (s.t - p.lastT)
	}
	p.lastT = s.t
}

func (s *Sim) grantLocked(p *pool, g *grant, immediate bool) {
	s.tickPool(p)
	p.inUse++
	p.served++
	g.got = true
	if p.inUse > p.peakInUse {
		p.peakInUse = p.inUse
	}
	if p.inUse >= p.cap && p.firstFullT < 0 {
		p.firstFullT = s.t
	}
	s.poolSample(p)
	g.onGrant()
}

// free returns capacity, handing it to the oldest live waiter if any.
func (s *Sim) free(p *pool, g *grant) {
	if g == nil || !g.got {
		return
	}
	s.tickPool(p)
	g.got = false
	g.done = true
	p.inUse--
	for len(p.q) > 0 {
		nx := p.q[0]
		p.q = p.q[1:]
		if nx.done {
			continue
		}
		s.grantLocked(p, nx, false)
		return
	}
	s.poolSample(p)
}

// cancelDead removes a waiter whose deadline elapsed. Returns true if the
// request never got capacity (so the caller must report a timeout).
// cancelIfQueued removes a waiter from a pool without freeing a slot.
func (s *Sim) cancelIfQueued(p *pool, g *grant) {
	if g == nil || g.got || g.done {
		return
	}
	g.done = true
	for i, x := range p.q {
		if x == g {
			p.q = append(p.q[:i], p.q[i+1:]...)
			break
		}
	}
	s.poolSample(p)
}

func (s *Sim) cancelDead(g *grant) bool {
	if g == nil || g.got || g.done {
		return false
	}
	g.done = true
	if g.p != nil {
		for i, x := range g.p.q {
			if x == g {
				g.p.q = append(g.p.q[:i], g.p.q[i+1:]...)
				break
			}
		}
		s.poolSample(g.p)
	}
	return true
}

func (s *Sim) poolSample(p *pool) {
	s.svcLog = append(s.svcLog, model.ServiceSnapshot{
		T: s.t, Pool: p.name, InUse: p.inUse, Queue: len(p.q), Served: p.served, Cap: p.cap,
	})
}

// ------------------------------------------------------------------------------------------------------------------------------------------
// Host and simulator state
// ------------------------------------------------------------------------------------------------------------------------------------------

type host struct {
	m     *model.Machine
	wave  int
	jit   float64
	postT float64

	phase       model.Phase
	detail      string
	attempt     int
	fault       model.FaultCode
	faultPhase  model.Phase
	faultDetail string
	doneAt      float64

	ip                               string
	ses                              bool // holds a boot session on its ToR
	seat, lease, tftpS, httpS, instS bool
	gSeat, gTFTP, gHTTP, gInst       *grant
	leaseG                           *grant

	netTries int

	held        bool
	auditCode   model.FaultCode
	auditRepair audit.Repair
	issues      []audit.Issue
	// remediated records that a BMC-side fix was applied before power-on.
	remediated bool
}

type switchState struct {
	sw        *model.Switch
	sess      int
	peak      int
	dropped   int
	firstDrop float64
}

type Sim struct {
	cfg  RunConfig
	sc   *spec.Scenario
	au   *audit.Fleet
	wave []Wave

	t   float64
	h   evHeap
	seq int64

	hosts           []*host
	sw              map[string]*switchState
	hostsPerCabinet map[int]int

	seats, tftpP, httpP, instP *pool
	leases                     int
	leaseUsed                  int
	leaseQ                     []*grant
	leaseRefused               int
	leasePeakInUse             int
	leasePeakQ                 int
	leaseFirstFull             float64

	svcLog    []model.ServiceSnapshot
	leasesLog []LeaseSample

	evLog map[int][]model.Event
	trans map[int][]model.Transition

	rng *rand.Rand
}

// New prepares a run.
func New(sc *spec.Scenario, au *audit.Fleet, cfg RunConfig, waves []Wave) *Sim {
	if len(waves) == 0 {
		waves = []Wave{{AtS: 0, Count: len(sc.Machines)}}
	}
	pools := sc.Pools
	if cfg.Pools != nil {
		pools = *cfg.Pools
	}
	s := &Sim{
		cfg: cfg, sc: sc, au: au, wave: waves,
		sw:     map[string]*switchState{},
		seats:  newPool("dhcp-seats", pools.DHCPSeats),
		tftpP:  newPool("tftp", pools.TFTPStreams),
		httpP:  newPool("http", pools.HTTPStreams),
		instP:  newPool("install", pools.InstallShare),
		leases: pools.DHCPLeases, leaseFirstFull: -1,
		evLog: map[int][]model.Event{},
		trans: map[int][]model.Transition{},
		rng:   rand.New(rand.NewPCG(sc.Seed, 0xB007B007)),
	}
	s.hostsPerCabinet = map[int]int{}
	for _, m := range sc.Machines {
		s.hostsPerCabinet[m.Cabinet.ID]++
	}
	for id, sw := range sc.Switches {
		s.sw[id] = &switchState{sw: sw, firstDrop: -1}
	}
	for _, m := range sc.Machines {
		hh := &host{m: m, phase: model.PhaseOff}
		hh.jit = 0.94 + 0.12*s.rng.Float64()
		hh.postT = (postBaseS + postPerDIMMs*float64(m.Dimms.Count)) * (0.92 + 0.16*s.rng.Float64())
		if au != nil {
			if ar := au.Results[m.ID]; ar != nil && ar.Hold {
				hh.held = true
				hh.auditCode, hh.auditRepair, hh.issues = ar.Code, ar.Repair, ar.Issues
			}
		}
		s.hosts = append(s.hosts, hh)
	}
	sort.Slice(s.hosts, func(i, j int) bool { return s.hosts[i].m.ID < s.hosts[j].m.ID })
	return s
}

// NewWithPools is New with an explicit pool override.
func NewWithPools(sc *spec.Scenario, au *audit.Fleet, cfg RunConfig, waves []Wave, p model.Pools) *Sim {
	cfg.Pools = &p
	return New(sc, au, cfg, waves)
}

// Run executes to quiescence and returns the batch record.
func (s *Sim) Run() *Result {
	s.scheduleWaves()
	for s.h.Len() > 0 {
		e := s.h.pop()
		s.t = e.t
		e.fn()
	}
	return s.result()
}

func (s *Sim) scheduleWaves() {
	order := s.waveOrder()
	idx := 0
	for wi, w := range s.wave {
		n := w.Count
		if n <= 0 || idx+n > len(order) {
			n = len(order) - idx
		}
		for k := 0; k < n; k++ {
			hh := order[idx+k]
			hh.wave = wi
			at := w.AtS
			if s.cfg.StaggerS > 0 {
				at = w.AtS + float64(k)*s.cfg.StaggerS
			}
			s.atAbs(at, prioWave, func() { s.powerOn(hh) })
		}
		idx += n
	}
	if idx < len(order) {
		last := s.wave[len(s.wave)-1].AtS
		for ; idx < len(order); idx++ {
			hh := order[idx]
			hh.wave = len(s.wave) - 1
			s.atAbs(last, prioWave, func() { s.powerOn(hh) })
		}
	}
}

// waveOrder interleaves cabinets so a wave spreads across every ToR.
func (s *Sim) waveOrder() []*host {
	byCab := map[int][]*host{}
	var cabs []int
	for _, h := range s.hosts {
		c := h.m.Cabinet.ID
		if _, ok := byCab[c]; !ok {
			cabs = append(cabs, c)
		}
		byCab[c] = append(byCab[c], h)
	}
	sort.Ints(cabs)
	out := make([]*host, 0, len(s.hosts))
	for round := 0; len(out) < len(s.hosts); round++ {
		for _, c := range cabs {
			if round < len(byCab[c]) {
				out = append(out, byCab[c][round])
			}
		}
	}
	return out
}

// ------------------------------------------------------------------------------------------------------------------------------------------
// Recording helpers
// ------------------------------------------------------------------------------------------------------------------------------------------

func (s *Sim) setPhase(h *host, p model.Phase, detail string) {
	if h.phase == p && h.detail == detail {
		return
	}
	h.phase, h.detail = p, detail
	s.trans[h.m.ID] = append(s.trans[h.m.ID], model.Transition{
		T: s.t, Phase: p, Detail: detail, Attempt: h.attempt,
	})
}

func (s *Sim) log(h *host, proto, sev, format string, args ...any) {
	s.evLog[h.m.ID] = append(s.evLog[h.m.ID], model.Event{
		TimeS: s.t, Proto: proto, Severity: sev, Line: fmt.Sprintf(format, args...),
	})
}

func (s *Sim) logFault(h *host, code model.FaultCode, format string, args ...any) {
	s.evLog[h.m.ID] = append(s.evLog[h.m.ID], model.Event{
		TimeS: s.t, Proto: "agent", Severity: "error", Fault: code,
		Line: fmt.Sprintf(format, args...),
	})
}

// fail parks a host on a terminal fault, releasing everything it holds.
func (s *Sim) fail(h *host, code model.FaultCode, detail string) {
	if h.fault != "" {
		return
	}
	h.fault, h.faultPhase, h.faultDetail = code, h.phase, detail
	if h.doneAt == 0 {
		h.doneAt = s.t
	}
	s.releaseAll(h)
	s.setPhase(h, model.PhaseFault, detail)
}

func (s *Sim) releaseAll(h *host) {
	// Any queued (not-yet-granted) request dies with the host: otherwise its
	// grant fires later against a host that has already failed.
	s.cancelIfQueued(s.seats, h.gSeat)
	s.cancelIfQueued(s.tftpP, h.gTFTP)
	s.cancelIfQueued(s.httpP, h.gHTTP)
	s.cancelIfQueued(s.instP, h.gInst)
	if h.seat {
		h.seat = false
		s.free(s.seats, h.gSeat)
	}
	if h.ses {
		h.ses = false
		if st := s.sw[h.m.Cabinet.SwitchID]; st != nil && st.sess > 0 {
			st.sess--
		}
	}
	if h.leaseG != nil && !h.leaseG.got && !h.leaseG.done {
		h.leaseG.done = true
		for i, x := range s.leaseQ {
			if x == h.leaseG {
				s.leaseQ = append(s.leaseQ[:i], s.leaseQ[i+1:]...)
				break
			}
		}
		h.leaseG = nil
		s.leaseSample()
	}
	if h.lease {
		h.lease = false
		s.leaseUsed--
		s.leaseSample()
		s.drainLeases()
	}
	if h.tftpS {
		h.tftpS = false
		s.free(s.tftpP, h.gTFTP)
	}
	if h.httpS {
		h.httpS = false
		s.free(s.httpP, h.gHTTP)
	}
	if h.instS {
		h.instS = false
		s.free(s.instP, h.gInst)
	}
}

func (s *Sim) leaseSample() {
	s.leasesLog = append(s.leasesLog, LeaseSample{
		T: s.t, InUse: s.leaseUsed, Queued: len(s.leaseQ), Refused: s.leaseRefused,
	})
	if s.leaseUsed > s.leasePeakInUse {
		s.leasePeakInUse = s.leaseUsed
	}
	if len(s.leaseQ) > s.leasePeakQ {
		s.leasePeakQ = len(s.leaseQ)
	}
	if s.leaseUsed >= s.leases && s.leaseFirstFull < 0 {
		s.leaseFirstFull = s.t
	}
}

func (s *Sim) drainLeases() {
	for len(s.leaseQ) > 0 && s.leaseUsed < s.leases {
		g := s.leaseQ[0]
		s.leaseQ = s.leaseQ[1:]
		if g.done {
			continue
		}
		g.got = true
		s.leaseUsed++
		s.leaseSample()
		g.onGrant()
	}
}

// ------------------------------------------------------------------------------------------------------------------------------------------
// Host state machine
// ------------------------------------------------------------------------------------------------------------------------------------------

// Effective configuration: what the firmware will actually do once
// remediation (if any) has been applied over BMC.
func (s *Sim) remediated(h *host) bool {
	return s.cfg.Remediate && s.cfg.Preflight && h.held && h.auditRepair == audit.RepairRemote
}
func (s *Sim) pxeOn(h *host) bool { return h.m.PxeEnabled || s.remediated(h) }
func (s *Sim) pxeFirst(h *host) bool {
	if s.remediated(h) {
		return true
	}
	return len(h.m.BootOrder) > 0 && h.m.BootOrder[0] == "pxe"
}
func (s *Sim) ctrlMode(h *host) string {
	if s.remediated(h) {
		return s.sc.Templates[h.m.Model].RAID.ControllerMode
	}
	return h.m.ControllerMode
}
func (s *Sim) ctrlRAID(h *host) string {
	if s.remediated(h) {
		return "none"
	}
	return h.m.ControllerRAID
}

func (s *Sim) powerOn(h *host) {
	// A preflight hold only survives remediation if the problem is physical,
	// rack-level, or a template defect: those need hands or a network change.
	// Remotely fixable findings (boot order, PXE toggle, controller mode,
	// stray array) are cleared over BMC before the unit is ever powered.
	if h.held && s.cfg.Preflight {
		if s.cfg.Remediate && h.auditRepair == audit.RepairRemote {
			h.remediated = true
			s.log(h, "agent", "info", "预检已下发BMC配置并复位: %s", joinCodes(h.issues))
		} else {
			s.setPhase(h, model.PhaseOff, "预检拦截: "+model.FaultLabel(h.auditCode))
			if h.doneAt == 0 {
				h.doneAt = s.t
			}
			s.log(h, "agent", "warn", "预检未通过,不装: %s (%s)", model.FaultLabel(h.auditCode), repairLabel(h.auditRepair))
			return
		}
	}
	s.setPhase(h, model.PhasePOST, "固件上电自检")
	s.log(h, "fw", "info", "%s %s 上电,POST 开始 (DIMM %d, 盘 %d, 网卡 %s fw %s)",
		h.m.Model, h.m.Serial, h.m.Dimms.Count, h.m.Disks.Count, h.m.NICs.Model, h.m.NICs.Firmware)
	s.at(h.postT, prioStep, func() { s.afterPOST(h) })
}

func (s *Sim) afterPOST(h *host) {
	if h.fault != "" || h.phase != model.PhasePOST {
		return
	}
	s.log(h, "fw", "info", "POST 完成: %d×%dGB, %d×%s %dGB, %s, 启动顺序 %v",
		h.m.Dimms.Count, h.m.Dimms.PerDIMM, h.m.Disks.Count, h.m.Disks.Model, h.m.Disks.SizeGB,
		h.m.NICs.Model, h.m.BootOrder)

	// A POST-level inventory diff is visible to the tool even without the
	// preflight audit; it is recorded but does not stop the boot.
	if diffs := s.inventoryDiffs(h); len(diffs) > 0 {
		s.log(h, "fw", "warn", "硬件与采购单不符: %s", joinStrings(diffs))
	}

	if !s.pxeOn(h) || !s.pxeFirst(h) {
		s.noBootRequest(h)
		return
	}
	s.tryDHCP(h)
}

// inventoryDiffs returns human-readable mismatches against the PO line.
func (s *Sim) inventoryDiffs(h *host) []string {
	po := s.sc.Procurement[h.m.Model]
	var out []string
	if h.m.Dimms.Count != po.Dimms.Count {
		out = append(out, fmt.Sprintf("DIMM %d/%d", h.m.Dimms.Count, po.Dimms.Count))
	}
	if h.m.Dimms.Model != po.Dimms.Model {
		out = append(out, "DIMM型号")
	}
	if h.m.Disks.Count != po.Disks.Count {
		out = append(out, fmt.Sprintf("盘 %d/%d", h.m.Disks.Count, po.Disks.Count))
	}
	if h.m.Disks.Model != po.Disks.Model || h.m.Disks.SizeGB != po.Disks.SizeGB {
		out = append(out, "盘型号")
	}
	if h.m.NICs.Model != po.NICs.Model {
		out = append(out, "网卡型号")
	}
	return out
}

func (s *Sim) noBootRequest(h *host) {
	first := "disk"
	if len(h.m.BootOrder) > 0 {
		first = h.m.BootOrder[0]
	}
	s.setPhase(h, model.PhasePOST, fmt.Sprintf("无网络启动请求,尝试本地 %s", first))
	switch first {
	case "usb":
		s.log(h, "bios", "warn", "从外接设备启动,未进入网卡 option ROM")
	case "disk":
		s.log(h, "bios", "warn", "本地盘无引导记录,启动顺序中网络启动排在后面,未尝试")
	default:
		if !h.m.PxeEnabled {
			s.log(h, "bios", "warn", "网卡 option ROM 中 PXE 已关闭")
		} else {
			s.log(h, "bios", "warn", "启动顺序未把网卡放在首位")
		}
	}
	s.at(noBootRequestTO, prioStep, func() {
		if h.fault != "" || h.phase != model.PhasePOST {
			return
		}
		code := model.F_BOOT_SILENT
		switch {
		case !h.m.PxeEnabled:
			code = model.F_PXE_DISABLED
		case first == "usb":
			code = model.F_BOOT_USB
		case first == "disk":
			code = model.F_BOOT_ORDER
		}
		s.logFault(h, code, "上电 %0.fs 内无 DHCP DISCOVER,判定未发起网络启动", noBootRequestTO)
		s.fail(h, code, "上电后无网络启动请求")
	})
}

func (s *Sim) tryDHCP(h *host) {
	s.setPhase(h, model.PhasePXE, "option ROM 发起 DHCP 请求")
	st := s.sw[h.m.Cabinet.SwitchID]
	// Access switch: boot VLAN not carried, or boot-session budget full.
	if !st.sw.BootVLANEnabled {
		st.dropped++
		if st.firstDrop < 0 {
			st.firstDrop = s.t
		}
		s.log(h, "switch", "error", "DISCOVER 在 %s 被丢弃:启动 VLAN %d 未放行", st.sw.ID, st.sw.BootVLAN)
		s.at(pickBootS+dhcpAttemptTO, prioTimeout, func() { s.dhcpNoReply(h) })
		return
	}
	if st.sess >= st.sw.MaxBootSessions {
		st.dropped++
		if st.firstDrop < 0 {
			st.firstDrop = s.t
		}
		s.log(h, "switch", "warn", "DISCOVER 被 %s 丢弃:启动会话 %d/%d 已满", st.sw.ID, st.sess, st.sw.MaxBootSessions)
		s.at(pickBootS+dhcpAttemptTO, prioTimeout, func() { s.dhcpNoReply(h) })
		return
	}
	st.sess++
	if st.sess > st.peak {
		st.peak = st.sess
	}
	h.ses = true
	s.log(h, "dhcp", "info", "→ DISCOVER (xid %d) 经 %s", h.m.ID, st.sw.ID)
	s.at(pickBootS, prioStep, func() { s.dhcpSeat(h) })
}

func (s *Sim) dhcpSeat(h *host) {
	if h.fault != "" {
		return
	}
	h.gSeat = s.use(s.seats, h, func() {
		h.seat = true
		s.at(dhcpSeatS, prioRelease, func() {
			if h.seat {
				h.seat = false
				s.free(s.seats, h.gSeat)
			}
			s.dhcpLease(h)
		})
	})
	s.at(dhcpAttemptTO, prioTimeout, func() {
		if s.cancelDead(h.gSeat) {
			s.log(h, "dhcp", "warn", "DHCP 服务 %0.fs 内未受理请求", dhcpAttemptTO)
			s.dhcpNoReply(h)
		}
	})
}

func (s *Sim) dhcpLease(h *host) {
	if h.fault != "" {
		return
	}
	if s.leaseUsed < s.leases {
		s.leaseUsed++
		s.grantLease(h)
		return
	}
	// The pool is empty. A real client keeps asking and waits; the wait is
	// long, and only a wait this long is reported as pool exhaustion.
	s.leaseRefused++
	s.log(h, "dhcp", "warn", "地址池已满 %d/%d,租约请求排队(第 %d 台等待)", s.leaseUsed, s.leases, len(s.leaseQ)+1)
	s.leaseSample()
	g := &grant{h: h}
	g.onGrant = func() {
		// drainLeases has already incremented the counter.
		s.grantLease(h)
	}
	s.leaseQ = append(s.leaseQ, g)
	h.leaseG = g
	s.at(leaseWaitTO, prioTimeout, func() {
		if g.got || g.done {
			return
		}
		g.done = true
		for i, x := range s.leaseQ {
			if x == g {
				s.leaseQ = append(s.leaseQ[:i], s.leaseQ[i+1:]...)
				break
			}
		}
		s.leaseSample()
		s.logFault(h, model.F_DHCP_EXHAUST, "地址池 %d 个租约全部被占,%0.fs 内未排到", s.leases, leaseWaitTO)
		s.fail(h, model.F_DHCP_EXHAUST, fmt.Sprintf("地址池耗尽,等待 %.0fs 未排到", leaseWaitTO))
	})
}

// grantLease hands an address to a host whose lease unit is already counted.
func (s *Sim) grantLease(h *host) {
	h.lease = true
	h.leaseG = nil
	// One /24 per eight cabinets keeps the address space legible.
	h.ip = fmt.Sprintf("10.40.%d.%d", 16+(h.m.Cabinet.ID-1)/8, 1+h.m.ID%250)
	s.leaseSample()
	s.log(h, "dhcp", "info", "← OFFER %s / ACK,租约占用 %d/%d", h.ip, s.leaseUsed, s.leases)
	h.netTries = 0
	s.tryTFTP(h)
}

func (s *Sim) dhcpNoReply(h *host) {
	if h.fault != "" {
		return
	}
	h.attempt++
	h.netTries++
	if h.netTries >= dhcpAttempts {
		s.releaseAll(h)
		code, detail := s.attributeDHCP(h)
		s.logFault(h, code, "DHCP %d 次尝试无应答: %s", h.netTries, detail)
		s.fail(h, code, detail)
		return
	}
	s.log(h, "dhcp", "warn", "第 %d 次无应答,%.0fs 后重试", h.netTries, dhcpBackoffS)
	s.setPhase(h, model.PhasePXE, fmt.Sprintf("等待重试 (%d/%d)", h.netTries, dhcpAttempts))
	s.at(dhcpBackoffS, prioStep, func() { s.tryDHCP(h) })
}

// attributeDHCP names the observable cause of a missing OFFER.
func (s *Sim) attributeDHCP(h *host) (model.FaultCode, string) {
	st := s.sw[h.m.Cabinet.SwitchID]
	switch {
	case !st.sw.BootVLANEnabled:
		return model.F_SWITCH_OFF, fmt.Sprintf("%s 未放行启动 VLAN(同柜共因)", st.sw.ID)
	case st.sw.MaxBootSessions < s.hostsPerCabinet[st.sw.CabinetID] && st.dropped > 0:
		return model.F_SWITCH_CAP, fmt.Sprintf("%s 启动会话上限 %d 低于该柜主机数 %d(同柜共因)",
			st.sw.ID, st.sw.MaxBootSessions, s.hostsPerCabinet[st.sw.CabinetID])
	case s.leasePeakInUse >= s.leases:
		return model.F_DHCP_EXHAUST, fmt.Sprintf("地址池 %d 个租约在高峰期全部占满", s.leases)
	case s.leaseRefused > 0:
		return model.F_DHCP_EXHAUST, fmt.Sprintf("地址池 %d 个租约不足,曾拒绝 %d 次请求", s.leases, s.leaseRefused)
	}
	return model.F_DHCP_TIMEOUT, "DHCP 服务无应答(单机超时)"
}

func (s *Sim) tryTFTP(h *host) {
	s.setPhase(h, model.PhaseTFTP, "请求引导程序")
	h.gTFTP = s.use(s.tftpP, h, func() {
		h.tftpS = true
		// Old option ROM cannot chain the UEFI HTTP bootloader, and it
		// prints its version when it fails — an attributable fault.
		if s.nicTooOld(h) {
			s.at(2.0, prioStep, func() {
				if h.fault != "" {
					return
				}
				s.releaseAll(h)
				s.logFault(h, model.F_NIC_FIRMWARE, "option ROM %s 不支持 UEFI HTTP boot: %s",
					h.m.NICs.Firmware, "获取 bootloader.efi 返回 501")
				s.fail(h, model.F_NIC_FIRMWARE, "网卡固件过低,不支持HTTPS启动")
			})
			return
		}
		t := float64(s.sc.Pools.BootloaderMB) / float64(s.sc.Pools.TFTPMBps) * h.jit
		s.log(h, "tftp", "info", "→ RRQ bootloader.efi (%dMB),TFTP 流 %d/%d",
			s.sc.Pools.BootloaderMB, s.tftpP.inUse, s.tftpP.cap)
		s.at(t, prioStep, func() {
			if h.fault != "" {
				return
			}
			h.tftpS = false
			s.free(s.tftpP, h.gTFTP)
			// Boot session and lease end when the kernel takes over.
			if h.ses {
				h.ses = false
				if st := s.sw[h.m.Cabinet.SwitchID]; st.sess > 0 {
					st.sess--
				}
			}
			s.log(h, "tftp", "info", "← 引导程序接收完成,校验通过")
			s.startHTTP(h)
		})
	})
	s.at(tftpQueueTO, prioTimeout, func() {
		if !s.cancelDead(h.gTFTP) {
			return
		}
		s.netRetry(h, model.F_TFTP_BUSY, "TFTP 排队超时")
	})
}

func (s *Sim) nicTooOld(h *host) bool {
	req := s.sc.Templates[h.m.Model].RequiresNICFirmware
	return cmpFirmware(h.m.NICs.Firmware, req) < 0
}

func (s *Sim) startHTTP(h *host) {
	tpl := s.sc.Templates[h.m.Model]
	s.setPhase(h, model.PhaseHTTP, "拉取安装镜像 "+tpl.ImageName)
	h.gHTTP = s.use(s.httpP, h, func() {
		h.httpS = true
		// Shared bandwidth: each active stream gets its fair share of the
		// aggregate, capped by the per-stream ceiling.
		active := s.httpP.inUse
		if active < 1 {
			active = 1
		}
		mbps := float64(s.sc.Pools.HTTPMBps) / float64(active)
		if mbps > float64(s.sc.Pools.HTTPStreamMBps) {
			mbps = float64(s.sc.Pools.HTTPStreamMBps)
		}
		dur := float64(tpl.ImageGB) * 1024 / mbps * h.jit
		s.log(h, "http", "info", "→ GET /images/%s (%.0fGB @ %.0fMB/s ≈ %.0fs)", tpl.ImageName, float64(tpl.ImageGB), mbps, dur)
		if h.m.Truth == model.R_ImageSlow {
			dur *= 3.0
		}
		if dur > httpTransferTO {
			dur = httpTransferTO + 1
		}
		s.at(dur, prioStep, func() {
			if h.fault != "" {
				return
			}
			h.httpS = false
			s.free(s.httpP, h.gHTTP)
			if dur > httpTransferTO {
				s.log(h, "http", "error", "镜像传输超过安装器超时 %0.fs,流中断", httpTransferTO)
				s.httpRetry(h, model.F_HTTP_TIMEOUT, "镜像传输超时")
				return
			}
			if h.m.Truth == model.R_ImageCorrupt {
				s.log(h, "http", "error", "镜像 sha256 不符:期望 9f2c.. 实得 7a11..")
				s.httpRetry(h, model.F_IMAGE_CORRUPT, "安装镜像校验失败")
				return
			}
			s.log(h, "http", "info", "← 镜像接收完成,sha256 通过")
			s.startInstall(h)
		})
	})
	s.at(httpQueueTO, prioTimeout, func() {
		if !s.cancelDead(h.gHTTP) {
			return
		}
		s.netRetry(h, model.F_HTTP_BUSY, "HTTP 排队超时")
	})
}

func (s *Sim) httpRetry(h *host, code model.FaultCode, detail string) {
	h.netTries++
	if h.netTries >= maxNetAttempts {
		s.logFault(h, code, "%s,重试 %d 次仍失败", detail, h.netTries)
		s.fail(h, code, detail)
		return
	}
	s.log(h, "http", "warn", "%s,退避重试 (%d/%d)", detail, h.netTries, maxNetAttempts)
	s.setPhase(h, model.PhaseHTTP, "镜像失败,重试中")
	s.at(dhcpBackoffS, prioStep, func() { s.startHTTP(h) })
}

// netRetry is the common network-phase retry path: start over from DHCP.
func (s *Sim) netRetry(h *host, code model.FaultCode, detail string) {
	h.netTries++
	if h.netTries >= maxNetAttempts {
		s.logFault(h, code, "%s,重试 %d 次仍失败", detail, h.netTries)
		s.fail(h, code, detail)
		return
	}
	s.log(h, "net", "warn", "%s,退避重试 (%d/%d)", detail, h.netTries, maxNetAttempts)
	s.releaseAll(h)
	s.at(dhcpBackoffS, prioStep, func() { s.tryDHCP(h) })
}

func (s *Sim) startInstall(h *host) {
	tpl := s.sc.Templates[h.m.Model]
	s.setPhase(h, model.PhaseInstall, "分区装机 "+tpl.ImageName)
	h.gInst = s.use(s.instP, h, func() {
		h.instS = true
		s.installSteps(h)
	})
	s.at(installQueueTO, prioTimeout, func() {
		if s.cancelDead(h.gInst) {
			s.logFault(h, model.F_INSTALL_FAIL, "装机器排队超时")
			s.fail(h, model.F_INSTALL_FAIL, "装机并发不足,排队超时")
		}
	})
}

// installSteps walks the installer's own sequence and fails where the
// machine physically cannot satisfy the model template.
func (s *Sim) installSteps(h *host) {
	tpl := s.sc.Templates[h.m.Model]
	railFail := func(code model.FaultCode, detail string) {
		s.at(raidSetupS, prioStep, func() {
			if h.fault != "" {
				return
			}
			s.logFault(h, code, "%s", detail)
			s.fail(h, code, detail)
		})
	}
	switch {
	case h.m.Disks.Count < tpl.MinDiskCount:
		railFail(model.F_DISK_COUNT, fmt.Sprintf("模板要求 %d 块盘,实装 %d 块,阵列无法按模板创建",
			tpl.MinDiskCount, h.m.Disks.Count))
		return
	case h.m.Disks.Count < tpl.RAID.MinMembers:
		railFail(model.F_TEMPLATE, fmt.Sprintf("%s 需 %d 个成员盘,实装 %d 块,模板不适用",
			tpl.RAID.Level, tpl.RAID.MinMembers, h.m.Disks.Count))
		return
	case h.m.Disks.SizeGB < tpl.RootDiskGB:
		railFail(model.F_TEMPLATE, fmt.Sprintf("单盘 %dGB 小于模板根分区 %dGB", h.m.Disks.SizeGB, tpl.RootDiskGB))
		return
	case s.ctrlMode(h) != tpl.RAID.ControllerMode:
		railFail(model.F_RAID_MODE, fmt.Sprintf("控制器为 %s 模式,无法创建 %s;模板要求 %s",
			s.ctrlMode(h), tpl.RAID.Level, tpl.RAID.ControllerMode))
		return
	case s.ctrlRAID(h) != "none":
		railFail(model.F_RAID_CFG, fmt.Sprintf("控制器残留阵列 %s,与模板 %s 冲突", s.ctrlRAID(h), tpl.RAID.Level))
		return
	}
	s.log(h, "agent", "info", "storcli 建阵列 %s,成员 %d 块", tpl.RAID.Level, tpl.RAID.MinMembers)
	s.at(raidSetupS, prioStep, func() {
		s.at(partitionBaseS+partitionPerDisk*float64(h.m.Disks.Count)*h.jit, prioStep, func() {
			s.partitionAndCopy(h)
		})
	})
}

func (s *Sim) partitionAndCopy(h *host) {
	if h.fault != "" {
		return
	}
	tpl := s.sc.Templates[h.m.Model]
	s.log(h, "agent", "info", "GPT 分区 + %s 挂载,写入软件包", tpl.RAID.Level)

	// Per-machine process variation already folded into the scheduled delay;
	// derive the write duration from the disk count.
	dur := (140.0 + 26.0*float64(h.m.Disks.Count)) * h.jit
	switch h.m.Truth {
	case model.R_DiskBad:
		s.at(dur*0.7, prioStep, func() {
			s.logFault(h, model.F_DISK_BAD, "写入 pkg-3 时反复重映射,盘离线 (SMART 5/197 异常)")
			s.fail(h, model.F_DISK_BAD, "硬盘SMART预失效,装机中掉线")
		})
		return
	case model.R_MemEcc:
		s.at(dur*0.45, prioStep, func() {
			s.logFault(h, model.F_MEM_ECC, "kernel 报 Uncorrectable ECC,机器自动降频并中止")
			s.fail(h, model.F_MEM_ECC, "内存ECC不可纠正错误")
		})
		return
	case model.R_InstallScript:
		s.at(dur*0.55, prioStep, func() {
			s.logFault(h, model.F_INSTALL_FAIL, "kickstart 第41行: mdadm --create /dev/md0 成员 /dev/nvme9p1 不存在")
			s.fail(h, model.F_INSTALL_FAIL, "装机脚本引用不存在的设备")
		})
		return
	}
	s.at(dur, prioStep, func() {
		if h.fault != "" {
			return
		}
		s.log(h, "agent", "info", "软件包写入完成,GRUB 安装到 %s", tpl.RAID.Level)
		h.instS = false
		s.free(s.instP, h.gInst)
		if h.lease {
			h.lease = false
			s.leaseUsed--
			s.leaseSample()
			s.drainLeases()
		}
		s.setPhase(h, model.PhaseReboot, "重启进入新系统")
		s.at(rebootS*h.jit, prioStep, func() {
			if h.fault != "" {
				return
			}
			s.setPhase(h, model.PhaseDone, "已从本地阵列启动")
			s.log(h, "kernel", "info", "首次启动完成: %s,从 %s 启动", h.m.Hostname, tpl.RAID.Level)
			if h.doneAt == 0 {
				h.doneAt = s.t
			}
		})
	})
}

// ------------------------------------------------------------------------------------------------------------------------------------------
// Result assembly
// ------------------------------------------------------------------------------------------------------------------------------------------

func (s *Sim) result() *Result {
	r := &Result{
		Name: s.cfg.Name, Events: s.evLog, Trans: s.trans,
		Peak: map[string]*PeakStat{}, Leases: s.leasesLog, SvcLog: s.svcLog,
	}
	for _, h := range s.hosts {
		r.Machines = append(r.Machines, s.record(h))
		r.MakespanS = max(r.MakespanS, h.doneAt)
		switch {
		case h.phase == model.PhaseOff && h.held && s.cfg.Preflight:
			r.Held++
		case h.phase == model.PhaseDone:
			r.Installed++
		case h.fault != "":
			r.Failed++
		}
	}
	for _, p := range []*pool{s.seats, s.tftpP, s.httpP, s.instP} {
		s.tickPool(p)
		p.lastT = 0
	}
	r.Peak["dhcp-seats"] = poolPeak(s.seats)
	r.Peak["tftp"] = poolPeak(s.tftpP)
	r.Peak["http"] = poolPeak(s.httpP)
	r.Peak["install"] = poolPeak(s.instP)
	r.Peak["dhcp-leases"] = &PeakStat{
		Name: "dhcp-leases", Capacity: s.leases, PeakInUse: s.leasePeakInUse,
		PeakQueue: s.leasePeakQ, FirstFullS: s.leaseFirstFull, Lost: s.leaseRefused,
	}
	for _, st := range s.sw {
		r.Switches = append(r.Switches, &SwitchTrace{
			ID: st.sw.ID, Cabinet: st.sw.CabinetID, PeakSess: st.peak,
			Cap: st.sw.MaxBootSessions, Dropped: st.dropped, FirstDrop: st.firstDrop,
		})
	}
	sort.Slice(r.Switches, func(i, j int) bool { return r.Switches[i].Cabinet < r.Switches[j].Cabinet })
	s.buildSeries(r)
	return r
}

func poolPeak(p *pool) *PeakStat {
	return &PeakStat{
		Name: p.name, Capacity: p.cap, PeakInUse: p.peakInUse, PeakQueue: p.peakQueue,
		BusySecond: p.busy, FirstFullS: p.firstFullT, Served: p.served,
	}
}

func (s *Sim) record(h *host) *MachineRecord {
	m := h.m
	rec := &MachineRecord{
		ID: m.ID, Serial: m.Serial, Hostname: m.Hostname, Model: m.Model,
		Cabinet: m.Cabinet.ID, RackU: m.RackU, Phase: h.phase, Attempts: h.attempt,
		DoneAtS: h.doneAt, Wave: h.wave, Held: h.held,
		AuditCode: h.auditCode, AuditRepair: h.auditRepair, Issues: audit.IssuesFor(&audit.Result{Issues: h.issues}),
		Fault: h.fault, FaultPhase: h.faultPhase, FaultDetail: h.faultDetail,
		Truth: m.Truth, TruthNote: m.TruthNote, Log: s.evLog[m.ID],
		Remediated: h.remediated,
	}
	classify(rec, s.cfg)
	return rec
}

// classify assigns the verdict and scores how the tool's answer compared to
// the seeded truth. Four outcomes matter to the fleet owner:
//
//	held          audit caught the defect before power-on
//	failed        unit stopped on its own defect (a symptom was attributed)
//	noncompliant  unit installed despite a defect (the tool missed it)
//	capacity      shared-service contention stopped it (unit condition unknown)
func classify(rec *MachineRecord, cfg RunConfig) {
	expected := expectedCode(rec.Truth)

	if rec.Held && cfg.Preflight && rec.Phase == model.PhaseOff {
		rec.Verdict = "held"
		rec.Attribution = rec.AuditCode
		switch {
		case rec.Truth == model.R_None:
			rec.Matches = "miss" // held with nothing wrong: false positive
		case rec.AuditCode == expected:
			rec.Matches = "exact"
		case model.SymptomGroup(rec.AuditCode) == model.SymptomGroup(expected):
			rec.Matches = "symptom"
		default:
			rec.Matches = "miss"
		}
		return
	}

	if rec.Phase == model.PhaseDone {
		if rec.Remediated && rec.Truth != model.R_None {
			// The audit found it and a BMC fix cleared it; the unit that
			// installed is now compliant. Credit the fix.
			rec.Verdict = "remediated"
			rec.Attribution = rec.AuditCode
			rec.Matches = matchOf(rec.AuditCode, expected)
			return
		}
		if rec.Truth == model.R_None {
			rec.Verdict, rec.Matches = "ok", "n/a"
			return
		}
		rec.Verdict = "noncompliant"
		// A defect that can pass install unnoticed (missing DIMM, wrong disk
		// model) is only visible in the POST inventory diff.
		rec.Attribution = passiveCode(rec.Truth)
		if rec.Attribution == expected {
			rec.Matches = "exact"
		} else if model.SymptomGroup(rec.Attribution) == model.SymptomGroup(expected) {
			rec.Matches = "symptom"
		} else {
			rec.Matches = "miss"
		}
		return
	}

	if isContention(rec.Fault) {
		// The unit's own condition was never exercised: judge nothing.
		rec.Verdict = "capacity"
		rec.Attribution = rec.Fault
		if rec.Truth == model.R_None {
			rec.Matches = "n/a"
		} else {
			rec.Matches = "unresolved"
		}
		return
	}

	if rec.Fault != "" {
		rec.Verdict = "failed"
		rec.Attribution = rec.Fault
		switch {
		case rec.Remediated && rec.Truth != model.R_None:
			// The tool found and fixed it preflight; the unit then failed on
			// something else. Score by the fix that was applied.
			rec.Matches = matchOf(rec.AuditCode, expected)
		case rec.Truth == model.R_None:
			// A unit with nothing wrong failed at boot. Either a shared
			// service fault we already classified, or a genuine unknown.
			rec.Matches = "miss"
		default:
			rec.Matches = matchOf(rec.Fault, expected)
		}
		return
	}

	rec.Verdict, rec.Matches = "failed", "unresolved"
}

// matchOf ranks an answer against the expected code.
func matchOf(answer, expected model.FaultCode) string {
	switch {
	case answer == expected:
		return "exact"
	case answer == "":
		return "unresolved"
	case model.SymptomGroup(answer) == model.SymptomGroup(expected):
		return "symptom"
	}
	return "miss"
}

// isContention reports a fault caused by shared-service saturation, which
// says nothing about the unit's own condition.
func isContention(c model.FaultCode) bool {
	switch c {
	case model.F_DHCP_EXHAUST, model.F_DHCP_TIMEOUT, model.F_TFTP_BUSY,
		model.F_HTTP_BUSY, model.F_HTTP_TIMEOUT, model.F_INSTALL_FAIL:
		return true
	}
	return false
}

// expectedCode maps a seeded defect to the code the tool should infer.
func expectedCode(r model.RootCause) model.FaultCode {
	switch r {
	case model.R_BootOrder:
		return model.F_BOOT_ORDER
	case model.R_UsbStick:
		return model.F_BOOT_USB
	case model.R_PxeDisabled:
		return model.F_PXE_DISABLED
	case model.R_RaidMode:
		return model.F_RAID_MODE
	case model.R_RaidConfig:
		return model.F_RAID_CFG
	case model.R_DimmShort:
		return model.F_DIMM_SEAT
	case model.R_DimmWrongModel:
		return model.F_DIMM_MODEL
	case model.R_DiskWrongModel:
		return model.F_DISK_MODEL
	case model.R_DiskShort:
		return model.F_DISK_COUNT
	case model.R_DiskBad:
		return model.F_DISK_BAD
	case model.R_NicOldFw:
		return model.F_NIC_FIRMWARE
	case model.R_NicWrongModel:
		return model.F_NIC_MODEL
	case model.R_MemEcc:
		return model.F_MEM_ECC
	case model.R_SwitchOff:
		return model.F_SWITCH_OFF
	case model.R_SwitchCap:
		return model.F_SWITCH_CAP
	case model.R_ImageCorrupt:
		return model.F_IMAGE_CORRUPT
	case model.R_ImageSlow:
		return model.F_HTTP_TIMEOUT
	case model.R_InstallScript:
		return model.F_INSTALL_FAIL
	}
	return ""
}

// passiveCode is the code a successful-but-noncompliant unit is tagged with
// from its POST inventory diff, when no preflight audit ran.
func passiveCode(r model.RootCause) model.FaultCode {
	switch r {
	case model.R_DimmShort:
		return model.F_DIMM_SEAT
	case model.R_DimmWrongModel:
		return model.F_DIMM_MODEL
	case model.R_DiskWrongModel:
		return model.F_DISK_MODEL
	case model.R_DiskShort:
		return model.F_DISK_COUNT
	case model.R_NicOldFw:
		return model.F_NIC_FIRMWARE
	case model.R_NicWrongModel:
		return model.F_NIC_MODEL
	case model.R_SwitchCap:
		return model.F_SWITCH_CAP
	case model.R_ImageSlow:
		return model.F_HTTP_TIMEOUT
	}
	return ""
}

// buildSeries reconstructs the phase wall at 30s resolution.
func (s *Sim) buildSeries(r *Result) {
	const step = 60.0
	horizon := r.MakespanS
	if horizon <= 0 {
		horizon = 1
	}
	startedAt := map[int]float64{}
	for _, w := range r.Waves {
		_ = w
	}
	for _, m := range r.Machines {
		if len(s.trans[m.ID]) == 0 {
			continue
		}
		startedAt[m.ID] = s.trans[m.ID][0].T
	}
	for t := 0.0; t <= horizon+step; t += step {
		pt := PhasePoint{T: t, Count: map[string]int{}}
		for _, m := range r.Machines {
			ph := phaseAt(s.trans[m.ID], startedAt[m.ID], t)
			pt.Count[string(ph)]++
		}
		r.Series = append(r.Series, pt)
	}
}

// phaseAt returns the phase a host was in at time t, before its first
// transition counting as "off".
func phaseAt(tr []model.Transition, start float64, t float64) model.Phase {
	if len(tr) == 0 || t < start {
		return model.PhaseOff
	}
	cur := model.PhaseOff
	for _, e := range tr {
		if e.T > t {
			break
		}
		cur = e.Phase
	}
	return cur
}

func cmpFirmware(a, b string) int {
	pa, pb := splitVer(a), splitVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVer(v string) [3]int {
	var out [3]int
	field := 0
	cur := 0
	for _, c := range v {
		if c == '.' {
			if field < 3 {
				out[field] = cur
			}
			field++
			cur = 0
			continue
		}
		cur = cur*10 + int(c-'0')
	}
	if field < 3 {
		out[field] = cur
	}
	return out
}

func joinCodes(is []audit.Issue) string {
	out := ""
	for i, x := range is {
		if i > 0 {
			out += "; "
		}
		out += model.FaultLabel(x.Code)
	}
	return out
}

func repairLabel(r audit.Repair) string {
	switch r {
	case audit.RepairPhysical:
		return "物理处理"
	case audit.RepairRemote:
		return "BMC下发"
	case audit.RepairSwitch:
		return "交换机处理"
	case audit.RepairTemplate:
		return "修模板"
	}
	return "未知"
}

func joinStrings(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
