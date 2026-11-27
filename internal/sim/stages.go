package sim

import (
	"fmt"

	"baremetal-rollout/internal/diagnose"
	"baremetal-rollout/internal/domain"
)

func (s *Simulation) stepMachine(m *domain.Machine) {
	if m.Xfer != nil {
		return // bandwidth and transfer completion are handled by stepTransfers
	}

	// A scheduled successful completion wins if it was scheduled before timeout.
	if m.NextCheckAtMS != 0 && s.TimeMS >= m.NextCheckAtMS &&
		(m.TimeoutAtMS == 0 || m.NextCheckAtMS <= m.TimeoutAtMS) {
		action := m.PendingAction
		m.PendingAction = ""
		m.NextCheckAtMS = 0
		if action != "" {
			s.completeAction(m, action)
			return
		}
		next := m.NextStage
		m.NextStage = ""
		if next != "" {
			s.transition(m, next)
		}
		return
	}

	if m.TimeoutAtMS != 0 && s.TimeMS >= m.TimeoutAtMS {
		s.handleQueueTimeout(m)
		return
	}
}

func (s *Simulation) enterStage(m *domain.Machine, stage domain.Stage) {
	switch stage {
	case domain.StagePowerOn:
		m.Status = domain.StatusRunning
		m.StartedAtMS = s.TimeMS
		s.log(m, stage, "Redfish/IPMI", "→ BMC", fmt.Sprintf("POST /redfish/v1/Systems/%s/Actions/ComputerSystem.Reset PowerOn", m.Serial), true, 120)
		s.log(m, stage, "PSU", "↔ power", "双电源自检通过，服务器上电", true, 0)
		s.schedule(m, 2_000, domain.StagePOST)

	case domain.StagePOST:
		s.log(m, stage, "UEFI/BIOS", "↔ firmware", "BIOS POST 开始：CPU、内存训练、PCIe 枚举", true, 0)
		s.schedule(m, s.randomBetween(24_000, 42_000), domain.StageBootOrder)

	case domain.StageBootOrder:
		s.log(m, stage, "UEFI variable", "→ BIOS", "读取 BootOrder/Boot#### 启动项", true, 35)
		first := ""
		if len(m.BootOrder) > 0 {
			first = m.BootOrder[0]
		}
		s.log(m, stage, "UEFI variable", "← BIOS", "BootOrder="+fmt.Sprint(m.BootOrder), true, 0)
		if m.Truth != nil && m.Truth.Code == domain.CodeBootOrderWrong {
			s.log(m, stage, "UEFI", "× disk", "尝试从 "+first+" 引导：未找到 ESP/Boot Loader；启动顺序中无可用 IPv4 PXE 项", false, 6_000)
			s.blockWithIssue(m, *m.Truth, "machine", m.ID)
			return
		}
		s.log(m, stage, "PXE", "↔ UNDI", "IPv4 PXE 启动项有效，UNDI 驱动初始化成功", true, 1_200)
		s.schedule(m, 1_500, domain.StageNICFirmware)

	case domain.StageNICFirmware:
		s.log(m, stage, "ethtool/devlink", "→ NIC", fmt.Sprintf("查询 %s 固件、PXE ROM 和链路状态", m.Hardware.NICs[0].Name), true, 180)
		if issue := diagnose.ValidateNIC(m); issue != nil {
			s.log(m, stage, "devlink", "← NIC", fmt.Sprintf("firmware=%s, min=%s, PXE=%s", m.Hardware.NICs[0].Firmware, m.Purchase.MinNICFirmware, m.Hardware.NICs[0].PXEROM), false, 0)
			s.log(m, stage, "PXE", "× NIC", "新版 iPXE 所需特性协商失败，引导程序加载前终止", false, 3_000)
			s.blockWithIssue(m, *issue, "machine", m.Hardware.NICs[0].Name)
			return
		}
		nic := m.Hardware.NICs[0]
		s.log(m, stage, "devlink", "← NIC", fmt.Sprintf("%s, FW %s, %dGbps link up, %s", nic.Model, nic.Firmware, nic.LinkSpeedGbps, nic.PXEROM), true, 220)
		s.schedule(m, 1_500, domain.StageDHCP)

	case domain.StageDHCP:
		s.enterDHCP(m)

	case domain.StageTFTPBoot:
		s.enterTFTP(m)

	case domain.StageHTTPBoot:
		attempt := m.Attempts[string(stage)] + 1
		m.Attempts[string(stage)] = attempt
		if attempt > 1 {
			m.Status = domain.StatusRetrying
		} else {
			m.Status = domain.StatusRunning
		}
		s.log(m, stage, "HTTP", "→ boot-service", fmt.Sprintf("GET /boot/%s/vmlinuz+initrd (Range/stream)", m.ModelID), true, 0)
		s.startXfer(m, "http", "vmlinuz+initrd", s.Profile.HTTPFileBytes, s.Profile.HTTPTimeoutMS)

	case domain.StageInventory:
		m.Status = domain.StatusRunning
		s.log(m, stage, "agent", "→ mini-os", "引导内存系统启动，启动硬件清点 agent", true, 0)
		s.scheduleAction(m, 8_000, "inventory_validate")

	case domain.StageRAIDPlan:
		m.Status = domain.StatusRunning
		s.log(m, stage, "storcli/Redfish", "→ RAID", "读取控制器模式、物理盘和现有 External/RAID 配置", true, 300)
		s.scheduleAction(m, 4_000, "raid_validate")

	case domain.StagePartition:
		tpl := s.Fleet.Templates[m.TemplateID]
		s.log(m, stage, "parted", "→ disk", "创建 GPT 分区表: "+partitionSummary(tpl), true, 1_200)
		s.log(m, stage, "mkfs", "→ disk", "格式化 EFI/boot/root/data 文件系统", true, 0)
		s.schedule(m, 18_000, domain.StageImageDownload)

	case domain.StageImageDownload:
		attempt := m.Attempts[string(stage)] + 1
		m.Attempts[string(stage)] = attempt
		if attempt > 1 {
			m.Status = domain.StatusRetrying
		} else {
			m.Status = domain.StatusRunning
		}
		s.log(m, stage, "HTTPS", "→ image-registry", fmt.Sprintf("GET /images/%s-ubuntu2404-node.qcow2.zst (%.1f GB)", m.ModelID, float64(s.Profile.ImageBytes)/1e9), true, 0)
		if attempt == 1 || m.HardDeadlineAtMS == 0 {
			m.HardDeadlineAtMS = s.TimeMS + s.Profile.ImageOverallDeadlineMS
		}
		imageTimeout := s.Profile.ImageTimeoutMS
		if remaining := m.HardDeadlineAtMS - s.TimeMS; remaining > 0 && remaining < imageTimeout {
			imageTimeout = remaining
		}
		s.startXfer(m, "image", "node-image.qcow2.zst", s.Profile.ImageBytes, imageTimeout)
		if m.ResumeBytes > 0 && m.ResumeBytes < m.Xfer.TotalBytes {
			m.Xfer.DoneBytes = m.ResumeBytes
			s.log(m, stage, "HTTPS/Range", "C→image-registry", fmt.Sprintf("从 %.2fGB 处断点续传，而不是重新下载整个镜像", float64(m.ResumeBytes)/1e9), true, 0)
		}

	case domain.StageInstallOS:
		m.Status = domain.StatusRunning
		s.log(m, stage, "installer", "→ disk", "镜像校验通过，写入分区、展开文件、配置 cloud-init", true, 0)
		s.schedule(m, s.randomBetween(105_000, 145_000), domain.StageReboot)

	case domain.StageReboot:
		s.log(m, stage, "sysrq/systemd", "↻", "系统安装完成，卸载文件系统并重启", true, 3_000)
		s.schedule(m, 18_000, domain.StageFinalBoot)

	case domain.StageFinalBoot:
		s.log(m, stage, "UEFI", "← disk", "BootOrder 命中新系统 EFI Boot Loader", true, 0)
		s.log(m, stage, "systemd", "↔ OS", "内核启动，网络/cloud-init/配置管理完成", true, 0)
		s.schedule(m, 25_000, domain.StageHealthCheck)

	case domain.StageHealthCheck:
		s.log(m, stage, "gRPC/HTTP", "→ controller", "上报 node ready、磁盘挂载、GPU/网卡健康状态", true, 800)
		s.scheduleAction(m, 9_000, "health_ok")
	}
}

func (s *Simulation) completeAction(m *domain.Machine, action string) {
	switch action {
	case "dhcp_complete":
		s.completeDHCP(m)
	case "tftp_start":
		s.completeTFTPStart(m)
	case "inventory_validate":
		s.validateInventory(m)
	case "raid_validate":
		s.validateRAID(m)
	case "health_ok":
		s.succeed(m)
	default:
		// Unknown action is a programming error; make it visible rather than
		// leaving the machine silently stuck.
		s.failOverload(m, domain.CodeUnknown, "simulator", "未知仿真动作: "+action)
	}
}

func (s *Simulation) enterDHCP(m *domain.Machine) {
	attempt := m.Attempts[string(domain.StageDHCP)] + 1
	m.Attempts[string(domain.StageDHCP)] = attempt
	if attempt > 1 {
		m.Status = domain.StatusRetrying
	} else {
		m.Status = domain.StatusRunning
	}
	s.log(m, domain.StageDHCP, "DHCP/UDP", "C→S", fmt.Sprintf("%s:68 → 255.255.255.255:67 Discover (chaddr %s, option60 PXEClient)", m.MAC, m.MAC), true, 0)

	rack := s.Fleet.Racks[m.RackIndex]
	if rack.Switch.MissingVLAN == rack.Switch.ProvisionVLAN {
		s.log(m, domain.StageDHCP, "802.1Q/ToR", "× "+rack.Switch.ID, fmt.Sprintf("端口 %s 属于 VLAN 101/200，未放通 %d；带 PXE tag 的 Discover 被丢弃", m.SwitchPort, rack.Switch.ProvisionVLAN), false, 50)
		m.TimeoutAtMS = s.TimeMS + s.Profile.DHCPTimeoutMS
		return
	}

	completion := s.DHCP.Submit(s.TimeMS)
	m.NextCheckAtMS = completion
	m.PendingAction = "dhcp_complete"
	m.TimeoutAtMS = s.TimeMS + s.Profile.DHCPTimeoutMS
	wait := completion - s.TimeMS
	if wait > 1000 {
		s.log(m, domain.StageDHCP, "IPAM/queue", "… dhcp-ipam", fmt.Sprintf("地址分配请求进入队列，预计服务端排队 %dms", wait), true, 0)
	}
}

func (s *Simulation) completeDHCP(m *domain.Machine) {
	s.DHCP.Completed++
	ip := fmt.Sprintf("10.20.%d.%d", m.RackIndex+1, m.UPosition+20)
	m.AssignedIP = ip
	latency := s.TimeMS - m.StageAtMS
	s.log(m, domain.StageDHCP, "DHCP/UDP", "S→C", fmt.Sprintf("Offer %s/24, gw 10.20.%d.1, next-server 10.10.0.10", ip, m.RackIndex+1), true, latency/3)
	s.log(m, domain.StageDHCP, "DHCP/ARP", "C→S", "Request: 接受该租约并请求 PXE boot file", true, latency/3)
	s.log(m, domain.StageDHCP, "DHCP/ARP", "S→C", "Ack: bootfile=snponly-x64.efi; option66/67 已下发", true, latency/3)
	m.TimeoutAtMS = 0
	s.schedule(m, 1_000, domain.StageTFTPBoot)
}

func (s *Simulation) handleQueueTimeout(m *domain.Machine) {
	switch m.Stage {
	case domain.StageDHCP:
		attempt := m.Attempts[string(domain.StageDHCP)]
		rack := s.Fleet.Racks[m.RackIndex]
		if rack.Switch.MissingVLAN == rack.Switch.ProvisionVLAN {
			s.log(m, domain.StageDHCP, "DHCP", "× timeout", fmt.Sprintf("第 %d 次 Discover 后 %dms 未收到 Offer/Ack", attempt, s.Profile.DHCPTimeoutMS), false, s.Profile.DHCPTimeoutMS)
			if attempt < s.Profile.DHCPMaxAttempts {
				m.Status = domain.StatusRetrying
				backoff := s.Profile.DHCPBackoffMS + s.randomBetween(0, 1_500)
				s.schedule(m, backoff, domain.StageDHCP)
				return
			}
			issue := domain.Issue{Code: domain.CodeSwitchVLANMissing, Category: "network", Message: "同机柜端口的装机 VLAN 未放通，DHCP Discover 被交换机丢弃", Evidence: map[string]string{"rack": m.RackID, "switch": rack.Switch.ID, "vlan": fmt.Sprint(rack.Switch.ProvisionVLAN)}}
			s.blockWithIssue(m, issue, "rack", rack.Switch.ID)
			return
		}

		s.DHCP.TimedOut++
		s.log(m, domain.StageDHCP, "DHCP", "× timeout", fmt.Sprintf("第 %d 次 Discover 后 %dms 未收到 Offer/Ack", attempt, s.Profile.DHCPTimeoutMS), false, s.Profile.DHCPTimeoutMS)
		if attempt < s.Profile.DHCPMaxAttempts {
			s.DHCP.Retries++
			m.Status = domain.StatusRetrying
			backoff := s.Profile.DHCPBackoffMS + s.randomBetween(0, 1_500)
			s.schedule(m, backoff, domain.StageDHCP)
			return
		}
		s.failOverload(m, domain.CodeDHCPOverload, "dhcp-ipam", "DHCP/IPAM 请求队列持续拥塞，超过最大重试次数仍未获得地址")

	case domain.StageTFTPBoot:
		s.TFTPSetup.TimedOut++
		s.handleTransferStageTimeout(m, domain.CodeTFTPOverload, "tftp-boot-files", "TFTP RRQ/数据传输持续超时，引导程序下载失败")
	}
}

func (s *Simulation) enterTFTP(m *domain.Machine) {
	attempt := m.Attempts[string(domain.StageTFTPBoot)] + 1
	m.Attempts[string(domain.StageTFTPBoot)] = attempt
	if attempt > 1 {
		m.Status = domain.StatusRetrying
	} else {
		m.Status = domain.StatusRunning
	}
	s.log(m, domain.StageTFTPBoot, "TFTP/UDP", "C→S", fmt.Sprintf("RRQ snponly-x64.efi octet, blksize 1468, tsize=%d", s.Profile.TFTPFileBytes), true, 0)
	completion := s.TFTPSetup.Submit(s.TimeMS)
	m.NextCheckAtMS = completion
	m.PendingAction = "tftp_start"
	m.TimeoutAtMS = s.TimeMS + s.Profile.TFTPTimeoutMS
}

func (s *Simulation) completeTFTPStart(m *domain.Machine) {
	s.TFTPSetup.Completed++
	s.log(m, domain.StageTFTPBoot, "TFTP/OACK", "S→C", "OACK: blksize=1468, windowsize=8；开始传输 PXE 引导程序", true, s.TimeMS-m.StageAtMS)
	s.startXfer(m, "tftp", "snponly-x64.efi", s.Profile.TFTPFileBytes, s.Profile.TFTPTimeoutMS)
}

func (s *Simulation) validateInventory(m *domain.Machine) {
	issues := diagnose.ValidateHardware(m)
	s.log(m, domain.StageInventory, "dmidecode/nvme/ethtool", "← inventory",
		fmt.Sprintf("发现 %d 条 DIMM/%dGB, %d 块硬盘, %d 块网卡", len(m.Hardware.DIMMs), m.Hardware.MemoryGB, len(m.Hardware.Disks), len(m.Hardware.NICs)), true, 0)
	if len(issues) > 0 {
		issue := issues[0]
		for _, item := range issues {
			s.log(m, domain.StageInventory, "purchase-check", "× mismatch", item.Message+" "+fmt.Sprint(item.Evidence), false, 0)
		}
		s.blockWithIssue(m, issue, "machine", m.Purchase.OrderID)
		return
	}
	s.log(m, domain.StageInventory, "purchase-check", "✓ pass", "CPU、内存、硬盘数量/型号、网卡固件均与采购单 "+m.Purchase.OrderID+" 一致", true, 0)
	s.transition(m, domain.StageRAIDPlan)
}

func (s *Simulation) validateRAID(m *domain.Machine) {
	issue := diagnose.ValidateRAID(m, s.Fleet.Templates)
	if issue != nil {
		s.log(m, domain.StageRAIDPlan, "template-check", "× blocked", issue.Message+" "+fmt.Sprint(issue.Evidence), false, 0)
		s.blockWithIssue(m, *issue, "machine", m.TemplateID)
		return
	}
	tpl := s.Fleet.Templates[m.TemplateID]
	for _, v := range tpl.Volumes {
		s.log(m, domain.StageRAIDPlan, "storcli", "→ RAID", fmt.Sprintf("按模板创建卷 %s level=%s drives=%d hotspare=%d", v.Name, v.Level, v.DiskCount, v.HotSpare), true, 2_500)
	}
	s.log(m, domain.StageRAIDPlan, "template-check", "✓ pass", "机型模板适用，控制器模式和磁盘条件满足", true, 0)
	s.transition(m, domain.StagePartition)
}

func partitionSummary(tpl domain.RAIDTemplate) string {
	out := ""
	for i, p := range tpl.Partitions {
		if i > 0 {
			out += ", "
		}
		size := "剩余空间"
		if p.SizeMB > 0 {
			size = fmt.Sprintf("%dMB", p.SizeMB)
		}
		out += fmt.Sprintf("%s(%s,%s)", p.Mount, p.FileSystem, size)
	}
	return out
}
