package sim

import (
	"fmt"

	"baremetal-rollout/internal/domain"
)

func (s *Simulation) startXfer(m *domain.Machine, service, file string, totalBytes, timeoutMS int64) {
	quality := m.TransferQuality
	if quality <= 0 {
		quality = 1
	}
	// Small attempt-to-attempt TCP/load variation around the stable quality.
	jitter := 0.94 + s.RNG.Float64()*0.12
	efficiency := quality * jitter
	if efficiency < 0.55 {
		efficiency = 0.55
	}
	if efficiency > 1.35 {
		efficiency = 1.35
	}
	m.Xfer = &domain.XferState{
		Service: service, File: file, TotalBytes: totalBytes, Efficiency: efficiency,
		StartedAtMS: s.TimeMS, TimeoutAtMS: s.TimeMS + timeoutMS,
	}
	m.NextCheckAtMS = 0
	m.TimeoutAtMS = 0
	m.PendingAction = ""
}

func (s *Simulation) stepTransfers(dtMS int64) {
	groups := map[string][]*domain.Machine{}
	for _, m := range s.Fleet.Machines {
		if m.Xfer == nil || isTerminal(m.Status) {
			continue
		}
		if s.TimeMS >= m.Xfer.TimeoutAtMS {
			meter := s.Transfers[m.Xfer.Service]
			if meter != nil {
				meter.Timeouts++
			}
			s.handleTransferStageTimeout(m, s.timeoutCodeFor(m.Stage), m.Xfer.Service,
				fmt.Sprintf("%s 传输超过 %dms 未完成", m.Xfer.File, s.timeoutForStage(m.Stage)))
			continue
		}
		groups[m.Xfer.Service] = append(groups[m.Xfer.Service], m)
	}

	dtSec := float64(dtMS) / 1000.0
	for service, ms := range groups {
		meter := s.Transfers[service]
		if meter == nil {
			continue
		}
		n := len(ms)
		if n > meter.PeakActive {
			meter.PeakActive = n
		}
		if n > meter.SafeStreams && meter.FirstSaturatedAtMS == 0 {
			meter.FirstSaturatedAtMS = s.TimeMS
		}

		perBps := s.allocateBps(service, n, meter)
		if perBps > meter.PeakPerStreamBps {
			meter.PeakPerStreamBps = perBps
		}
		if meter.WorstPerStreamBps == 0 || perBps < meter.WorstPerStreamBps {
			meter.WorstPerStreamBps = perBps
		}

		for _, m := range ms {
			if m.Xfer == nil {
				continue
			}
			nodeBps := perBps * m.Xfer.Efficiency
			inc := int64(nodeBps * dtSec)
			if inc < 1 {
				inc = 1
			}
			m.Xfer.DoneBytes += inc
			m.Xfer.SpeedBps = nodeBps
			m.Progress = float64(domain.StageOrderIndex(m.Stage))/float64(len(domain.SortedStages())) +
				0.03*(float64(m.Xfer.DoneBytes)/float64(m.Xfer.TotalBytes))
			meter.BytesTransferred += inc
			if m.Xfer.DoneBytes >= m.Xfer.TotalBytes {
				m.Xfer.DoneBytes = m.Xfer.TotalBytes
				s.completeXfer(m)
			}
		}
	}
}

func (s *Simulation) allocateBps(service string, n int, meter *TransferMeter) float64 {
	switch service {
	case "tftp":
		per := meter.EgressBps / float64(n)
		if per > s.Profile.TFTPSessionBps {
			per = s.Profile.TFTPSessionBps
		}
		if n > meter.HardStreams {
			// Small-block UDP/ACK and session-table thrash beyond configured
			// maximum concurrent sessions.
			per *= float64(meter.HardStreams) / float64(n) * 0.78
		}
		return per
	default:
		safe := meter.SafeStreams
		if safe < 1 {
			safe = 1
		}
		efficiency := 1.0
		if n > safe {
			ratio := float64(n) / float64(safe)
			efficiency = 1.0 / (1.0 + 0.22*(ratio-1.0))
		}
		return meter.EgressBps * efficiency / float64(n)
	}
}

func (s *Simulation) completeXfer(m *domain.Machine) {
	service := m.Xfer.Service
	elapsed := s.TimeMS - m.Xfer.StartedAtMS
	bps := float64(m.Xfer.TotalBytes) / (float64(maxInt64(elapsed, 1)) / 1000.0)
	if meter := s.Transfers[service]; meter != nil {
		meter.Completed++
	}
	m.Xfer = nil

	switch m.Stage {
	case domain.StageTFTPBoot:
		s.log(m, m.Stage, "TFTP/DATA", "S→C", fmt.Sprintf("snponly-x64.efi 传输完成，平均 %.2f MB/s，耗时 %.1fs", bps/1e6, float64(elapsed)/1000), true, elapsed)
		s.log(m, m.Stage, "TFTP/ACK", "C→S", "最后 block ACK；UEFI 开始执行 iPXE", true, 0)
		s.schedule(m, 900, domain.StageHTTPBoot)

	case domain.StageHTTPBoot:
		s.log(m, m.Stage, "HTTP", "200 OK", fmt.Sprintf("vmlinuz/initrd 共 %.1fMB 下载完成，平均 %.2f MB/s", float64(s.Profile.HTTPFileBytes)/1e6, bps/1e6), true, elapsed)
		s.log(m, m.Stage, "kexec", "UEFI→kernel", "校验 initrd 签名并 kexec 进入安装内存系统", true, 1_500)
		s.schedule(m, 1_000, domain.StageInventory)

	case domain.StageImageDownload:
		m.ResumeBytes = 0
		s.log(m, m.Stage, "HTTPS", "200 OK", fmt.Sprintf("镜像 %.1fGB 拉取完成，平均 %.2f MB/s；SHA256 校验通过", float64(s.Profile.ImageBytes)/1e9, bps/1e6), true, elapsed)
		s.transition(m, domain.StageInstallOS)
	}
}

func (s *Simulation) handleTransferStageTimeout(m *domain.Machine, code, service, detail string) {
	stage := m.Stage
	maxAttempts, backoff := s.retryPolicy(stage)
	attempt := m.Attempts[string(stage)]
	resumeBytes := int64(0)
	if m.Xfer != nil {
		resumeBytes = m.Xfer.DoneBytes
	}
	s.log(m, stage, "TIMEOUT", "× "+service, fmt.Sprintf("第 %d 次尝试：%s", attempt, detail), false, s.timeoutForStage(stage))
	m.Xfer = nil

	if stage == domain.StageImageDownload {
		m.ResumeBytes = resumeBytes
	}

	if stage == domain.StageImageDownload && m.HardDeadlineAtMS > 0 && s.TimeMS >= m.HardDeadlineAtMS {
		s.failOverload(m, code, service, domain.CodeLabels[code]+"：超过镜像拉取总维护窗口，已停止重试并转人工/救援系统")
		return
	}

	if attempt < maxAttempts {
		m.Status = domain.StatusRetrying
		// Exponential-ish backoff plus jitter prevents all timed-out clients
		// from re-submitting in the same tick.
		backoffMS := backoff*int64(attempt) + s.randomBetween(0, backoff)
		s.log(m, stage, "retry", "↻", fmt.Sprintf("%.1fs 后进行第 %d/%d 次重试", float64(backoffMS)/1000, attempt+1, maxAttempts), true, 0)
		s.schedule(m, backoffMS, stage)
		return
	}
	s.failOverload(m, code, service, domain.CodeLabels[code]+"："+detail+"，已耗尽重试")
}

func (s *Simulation) retryPolicy(stage domain.Stage) (maxAttempts int, backoffMS int64) {
	switch stage {
	case domain.StageTFTPBoot:
		return s.Profile.TFTPMaxAttempts, s.Profile.TFTPBackoffMS
	case domain.StageHTTPBoot:
		return s.Profile.HTTPMaxAttempts, 5_000
	case domain.StageImageDownload:
		return s.Profile.ImageMaxAttempts, 15_000
	default:
		return 2, 3_000
	}
}

func (s *Simulation) timeoutForStage(stage domain.Stage) int64 {
	switch stage {
	case domain.StageTFTPBoot:
		return s.Profile.TFTPTimeoutMS
	case domain.StageHTTPBoot:
		return s.Profile.HTTPTimeoutMS
	case domain.StageImageDownload:
		return s.Profile.ImageTimeoutMS
	default:
		return 0
	}
}

func (s *Simulation) timeoutCodeFor(stage domain.Stage) string {
	switch stage {
	case domain.StageTFTPBoot:
		return domain.CodeTFTPOverload
	case domain.StageHTTPBoot:
		return domain.CodeHTTPOverload
	case domain.StageImageDownload:
		return domain.CodeImageOverload
	default:
		return domain.CodeUnknown
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
