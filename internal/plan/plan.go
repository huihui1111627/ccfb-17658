package plan

import (
	"fmt"
	"math"

	"baremetal-rollout/internal/domain"
)

type Profile struct {
	DHCPRatePerSec  float64 `json:"dhcpRatePerSec"`
	DHCPTimeoutMS   int64   `json:"dhcpTimeoutMs"`
	DHCPMaxAttempts int     `json:"dhcpMaxAttempts"`
	DHCPBackoffMS   int64   `json:"dhcpBackoffMs"`

	TFTPSetupRatePerSec float64 `json:"tftpSetupRatePerSec"`
	TFTPMaxSessions     int     `json:"tftpMaxSessions"`
	TFTPFileBytes       int64   `json:"tftpFileBytes"`
	TFTPSessionBps      float64 `json:"tftpSessionBps"`
	TFTPTimeoutMS       int64   `json:"tftpTimeoutMs"`
	TFTPMaxAttempts     int     `json:"tftpMaxAttempts"`
	TFTPBackoffMS       int64   `json:"tftpBackoffMs"`

	HTTPEgressBps    float64 `json:"httpEgressBps"`
	HTTPFileBytes    int64   `json:"httpFileBytes"`
	HTTPMinStreamBps float64 `json:"httpMinStreamBps"`
	HTTPTimeoutMS    int64   `json:"httpTimeoutMs"`
	HTTPMaxAttempts  int     `json:"httpMaxAttempts"`

	ImageEgressBps         float64 `json:"imageEgressBps"`
	ImageBytes             int64   `json:"imageBytes"`
	ImageSafeStreams       int     `json:"imageSafeStreams"`
	ImageMinStreamBps      float64 `json:"imageMinStreamBps"`
	ImageTimeoutMS         int64   `json:"imageTimeoutMs"`
	ImageOverallDeadlineMS int64   `json:"imageOverallDeadlineMs"`
	ImageMaxAttempts       int     `json:"imageMaxAttempts"`

	PowerRatePerSec float64 `json:"powerRatePerSec"`
	TargetUtil      float64 `json:"targetUtil"`
}

func DefaultProfile() Profile {
	return Profile{
		// Central DHCP/IPAM service with database + audit webhook latency.
		DHCPRatePerSec: 62, DHCPTimeoutMS: 10_000, DHCPMaxAttempts: 6, DHCPBackoffMS: 2_000,

		// Active/active TFTP pair, conservative UDP/block handling budget.
		TFTPSetupRatePerSec: 95, TFTPMaxSessions: 150,
		TFTPFileBytes: 6_500_000, TFTPSessionBps: 1_700_000,
		TFTPTimeoutMS: 24_000, TFTPMaxAttempts: 6, TFTPBackoffMS: 2_500,

		// Boot kernel + initrd over HTTP.
		HTTPEgressBps: 2_600_000_000, HTTPFileBytes: 180_000_000,
		HTTPMinStreamBps: 18_000_000, HTTPTimeoutMS: 240_000, HTTPMaxAttempts: 3,

		// Two image repositories behind 40 Gbps-class aggregate egress.
		ImageEgressBps: 4_500_000_000, ImageBytes: 12_000_000_000,
		ImageSafeStreams: 72, ImageMinStreamBps: 25_000_000,
		ImageTimeoutMS: 3_300_000, ImageOverallDeadlineMS: 3_300_000, ImageMaxAttempts: 1,

		PowerRatePerSec: 8, TargetUtil: 0.72,
	}
}

type Batch struct {
	Index         int            `json:"index"`
	Size          int            `json:"size"`
	MachineIDs    []string       `json:"machineIds"`
	RackCounts    map[string]int `json:"rackCounts"`
	StartOffsetMS int64          `json:"startOffsetMs"`
	PowerWindowMS int64          `json:"powerWindowMs"`
}

type Bottleneck struct {
	Stage       domain.Stage `json:"stage"`
	Service     string       `json:"service"`
	SafeLoad    float64      `json:"safeLoad"`
	OfferedLoad float64      `json:"offeredLoad"`
	Utilization float64      `json:"utilization"`
	FirstAtMS   int64        `json:"firstAtMs"`
	Severity    string       `json:"severity"`
	Detail      string       `json:"detail"`
}

type Plan struct {
	BatchSize           int          `json:"batchSize"`
	BatchCount          int          `json:"batchCount"`
	PowerRatePerSec     float64      `json:"powerRatePerSec"`
	PowerIntervalMS     int64        `json:"powerIntervalMs"`
	WaveSpacingMS       int64        `json:"waveSpacingMs"`
	CriticalPathMS      int64        `json:"criticalPathMs"`
	EstimatedTotalMS    int64        `json:"estimatedTotalMs"`
	Batches             []Batch      `json:"batches"`
	ForecastBottlenecks []Bottleneck `json:"forecastBottlenecks"`
	Rationale           []string     `json:"rationale"`
}

func Build(f *domain.Fleet, p Profile) Plan {
	imageByBandwidth := int(p.ImageEgressBps / p.ImageMinStreamBps)
	batchSize := minInt(p.ImageSafeStreams, imageByBandwidth)
	batchSize = minInt(batchSize, int(float64(p.TFTPMaxSessions)*0.70))
	batchSize = minInt(batchSize, int(p.DHCPRatePerSec*12)) // no more than ~12s of DHCP capacity burst
	if batchSize < 1 {
		batchSize = 1
	}

	batches := scheduleRoundRobin(f.Machines, f.Racks, batchSize)
	imageSafeBps := p.ImageEgressBps / float64(batchSize)
	imageMS := int64(float64(p.ImageBytes) / imageSafeBps * 1000)
	httpMS := int64(float64(p.HTTPFileBytes) / (p.HTTPEgressBps / float64(batchSize)) * 1000)
	tftpMS := int64(float64(p.TFTPFileBytes) / p.TFTPSessionBps * 1000)
	critical := int64(42_000) + 6_000 + 2_000 + tftpMS + httpMS + 8_000 + 5_000 + 20_000 +
		imageMS + 120_000 + 20_000 + 25_000 + 10_000
	waveSpacing := critical + 75_000

	for i := range batches {
		batches[i].Index = i + 1
		batches[i].StartOffsetMS = int64(i) * waveSpacing
		batches[i].PowerWindowMS = int64(float64(batches[i].Size) / p.PowerRatePerSec * 1000)
		for _, id := range batches[i].MachineIDs {
			for _, m := range f.Machines {
				if m.ID == id {
					m.Batch = i + 1
					break
				}
			}
		}
	}

	total := critical
	if len(batches) > 1 {
		total = int64(len(batches)-1)*waveSpacing + critical
	}

	forecast := ForecastAllAtOnce(f.Total, p)
	rationale := []string{
		fmt.Sprintf("镜像服务按每路至少 %.0f MB/s 的稳定拉取速度计算，安全并发为 %d 台。", p.ImageMinStreamBps/1_000_000, imageByBandwidth),
		fmt.Sprintf("TFTP 安全会话水位取最大会话数的 70%%，即 %d 台；批大小进一步受镜像并发约束。", int(float64(p.TFTPMaxSessions)*0.70)),
		fmt.Sprintf("建议每批 %d 台，以 %.0f 台/秒错峰上电，避免 POST 结束时刻形成 DHCP 突发。", batchSize, p.PowerRatePerSec),
		"批次内跨机柜轮询分布，避免单机柜 ToR 上行和 PDU 形成局部热点。",
		"仿真采用完成屏障：上一批健康机器完成、问题机器终态后再放下一批；预计波次间隔 " + formatMS(waveSpacing) + "。",
	}

	return Plan{
		BatchSize: batchSize, BatchCount: len(batches), PowerRatePerSec: p.PowerRatePerSec,
		PowerIntervalMS: int64(1000 / p.PowerRatePerSec), WaveSpacingMS: waveSpacing,
		CriticalPathMS: critical, EstimatedTotalMS: total, Batches: batches,
		ForecastBottlenecks: forecast, Rationale: rationale,
	}
}

func scheduleRoundRobin(machines []*domain.Machine, racks []domain.Rack, batchSize int) []Batch {
	byRack := make([][]*domain.Machine, len(racks))
	for _, m := range machines {
		byRack[m.RackIndex] = append(byRack[m.RackIndex], m)
	}
	positions := make([]int, len(byRack))
	var batches []Batch
	current := Batch{RackCounts: map[string]int{}}

	for {
		progress := false
		for ri := range byRack {
			if positions[ri] >= len(byRack[ri]) {
				continue
			}
			progress = true
			m := byRack[ri][positions[ri]]
			positions[ri]++
			current.MachineIDs = append(current.MachineIDs, m.ID)
			current.Size++
			current.RackCounts[m.RackID]++
			if current.Size == batchSize {
				batches = append(batches, current)
				current = Batch{RackCounts: map[string]int{}}
			}
		}
		if !progress {
			break
		}
	}
	if current.Size > 0 {
		batches = append(batches, current)
	}
	return batches
}

func ForecastAllAtOnce(total int, p Profile) []Bottleneck {
	postWindowSec := 18.0
	dhcpArrival := float64(total) / postWindowSec
	tftpDurationSec := float64(p.TFTPFileBytes) / p.TFTPSessionBps
	tftpConcurrency := dhcpArrival * tftpDurationSec
	httpSafe := p.HTTPEgressBps / p.HTTPMinStreamBps

	out := []Bottleneck{
		{
			Stage: domain.StageDHCP, Service: "dhcp-ipam", SafeLoad: p.DHCPRatePerSec * p.TargetUtil,
			OfferedLoad: dhcpArrival, Utilization: dhcpArrival / p.DHCPRatePerSec,
			FirstAtMS: 28_000, Severity: severity(dhcpArrival/p.DHCPRatePerSec, 0.75, 1.0),
			Detail: fmt.Sprintf("POST 结束窗口约 %.0f 秒，峰值约 %.0f leases/s；重试会进一步放大队列。", postWindowSec, dhcpArrival),
		},
		{
			Stage: domain.StageTFTPBoot, Service: "tftp-boot-files", SafeLoad: float64(p.TFTPMaxSessions) * p.TargetUtil,
			OfferedLoad: tftpConcurrency, Utilization: tftpConcurrency / float64(p.TFTPMaxSessions),
			FirstAtMS: 42_000, Severity: severity(tftpConcurrency/float64(p.TFTPMaxSessions), 0.75, 1.0),
			Detail: fmt.Sprintf("按 Little 定律估算 TFTP 平均并发约 %.0f，超过安全会话水位后出现 RRQ 超时和重试风暴。", tftpConcurrency),
		},
		{
			Stage: domain.StageHTTPBoot, Service: "http-boot-media", SafeLoad: httpSafe * p.TargetUtil,
			OfferedLoad: float64(total) * 0.88, Utilization: float64(total) * 0.88 / httpSafe,
			FirstAtMS: 75_000, Severity: severity(float64(total)*0.88/httpSafe, 0.75, 1.0),
			Detail: "Kernel/Initrd 文件较大，瞬时并发下降会拉长引导时间，但通常不是最早失效点。",
		},
		{
			Stage: domain.StageImageDownload, Service: "image-registry", SafeLoad: float64(p.ImageSafeStreams),
			OfferedLoad: float64(total) * 0.82, Utilization: float64(total) * 0.82 / float64(p.ImageSafeStreams),
			FirstAtMS: 240_000, Severity: "critical",
			Detail: fmt.Sprintf("约 %.0f 台同时拉取 %.1fGB 镜像，远高于 %d 路安全并发；每路带宽被摊薄并可能触发整体拉取超时。", float64(total)*0.82, float64(p.ImageBytes)/1e9, p.ImageSafeStreams),
		},
	}
	return out
}

func severity(u, watch, critical float64) string {
	if u >= critical {
		return "critical"
	}
	if u >= watch {
		return "watch"
	}
	return "ok"
}

func minInt(a, b int) int {
	return int(math.Min(float64(a), float64(b)))
}

func formatMS(ms int64) string {
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%d秒", sec)
	}
	return fmt.Sprintf("%d分%02d秒", sec/60, sec%60)
}
