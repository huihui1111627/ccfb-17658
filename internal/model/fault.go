package model

// FaultCode is the tool's attribution class for a machine that did not reach
// a compliant installed state. Exactly one primary code per machine.
type FaultCode string

const (
	F_BOOT_ORDER   FaultCode = "boot.order"    // boot order tried local disk first
	F_BOOT_USB     FaultCode = "boot.usbstick" // bootable foreign device first
	F_BOOT_SILENT  FaultCode = "boot.silent"   // powered, no network boot at all
	F_PXE_DISABLED FaultCode = "firmware.pxedisabled"
	F_RAID_MODE    FaultCode = "firmware.raidmode"
	F_TEMPLATE     FaultCode = "template.mismatch"
	F_RAID_CFG     FaultCode = "install.raidcfg"

	F_DIMM_SEAT    FaultCode = "hw.dimm.count"
	F_DIMM_MODEL   FaultCode = "hw.dimm.model"
	F_DISK_MODEL   FaultCode = "hw.disk.model"
	F_DISK_COUNT   FaultCode = "hw.disk.count"
	F_NIC_FIRMWARE FaultCode = "hw.nic.firmware"
	F_NIC_MODEL    FaultCode = "hw.nic.model"
	F_DISK_BAD     FaultCode = "hw.disk.smart"
	F_MEM_ECC      FaultCode = "hw.mem.ecc"

	F_SWITCH_OFF    FaultCode = "net.switch.off"
	F_SWITCH_CAP    FaultCode = "net.switch.cap"
	F_DHCP_EXHAUST  FaultCode = "dhcp.exhausted"
	F_DHCP_TIMEOUT  FaultCode = "dhcp.timeout"
	F_TFTP_BUSY     FaultCode = "tftp.capacity"
	F_TFTP_CORRUPT  FaultCode = "tftp.corrupt"
	F_HTTP_BUSY     FaultCode = "http.capacity"
	F_HTTP_TIMEOUT  FaultCode = "http.timeout"
	F_IMAGE_CORRUPT FaultCode = "image.corrupt"

	F_INSTALL_FAIL FaultCode = "install.fail"
)

// FaultCategory groups codes into the layers a floor engineer thinks in.
func FaultCategory(c FaultCode) string {
	switch c {
	case F_BOOT_ORDER, F_BOOT_USB, F_BOOT_SILENT, F_PXE_DISABLED, F_RAID_MODE, F_TEMPLATE, F_RAID_CFG:
		return "配置/模板"
	case F_DIMM_SEAT, F_DIMM_MODEL, F_DISK_MODEL, F_DISK_COUNT, F_NIC_FIRMWARE, F_NIC_MODEL, F_DISK_BAD, F_MEM_ECC:
		return "硬件"
	case F_SWITCH_OFF, F_SWITCH_CAP, F_DHCP_EXHAUST, F_DHCP_TIMEOUT, F_TFTP_BUSY, F_TFTP_CORRUPT, F_HTTP_BUSY, F_HTTP_TIMEOUT, F_IMAGE_CORRUPT:
		return "网络/服务"
	default:
		return "装机过程"
	}
}

// FaultLabel is the Chinese UI label for a fault class.
func FaultLabel(c FaultCode) string {
	switch c {
	case F_BOOT_ORDER:
		return "启动顺序错误(本地盘优先)"
	case F_BOOT_USB:
		return "启动顺序错误(外接设备优先)"
	case F_BOOT_SILENT:
		return "上电后无网络启动请求"
	case F_PXE_DISABLED:
		return "网卡网络启动被禁用"
	case F_RAID_MODE:
		return "阵列控制器处于AHCI模式"
	case F_TEMPLATE:
		return "硬盘配置不满足机型模板"
	case F_RAID_CFG:
		return "阵列配置与模板不符"
	case F_DIMM_SEAT:
		return "内存条数与采购单不符"
	case F_DIMM_MODEL:
		return "内存型号与采购单不符"
	case F_DISK_MODEL:
		return "硬盘型号/容量与采购单不符"
	case F_DISK_COUNT:
		return "硬盘数量与模板/采购单不符"
	case F_NIC_FIRMWARE:
		return "网卡固件低于模板要求"
	case F_NIC_MODEL:
		return "网卡型号与采购单不符"
	case F_DISK_BAD:
		return "硬盘SMART故障"
	case F_MEM_ECC:
		return "内存ECC错误"
	case F_SWITCH_OFF:
		return "机柜交换机未放行启动网络"
	case F_SWITCH_CAP:
		return "机柜交换机启动会话已满"
	case F_DHCP_EXHAUST:
		return "地址池耗尽"
	case F_DHCP_TIMEOUT:
		return "地址请求超时"
	case F_TFTP_BUSY:
		return "引导文件服务排队超时"
	case F_TFTP_CORRUPT:
		return "引导程序校验失败"
	case F_HTTP_BUSY:
		return "镜像服务排队超时"
	case F_HTTP_TIMEOUT:
		return "镜像传输中断"
	case F_IMAGE_CORRUPT:
		return "安装镜像校验失败"
	case F_INSTALL_FAIL:
		return "装机脚本/软件包失败"
	}
	return string(c)
}

// RootCause is the defect deliberately built into a unit. It is ground truth
// for scoring; the tool must infer an equivalent FaultCode from behaviour.
type RootCause string

const (
	R_None           RootCause = ""
	R_BootOrder      RootCause = "seeded.boot.order"
	R_UsbStick       RootCause = "seeded.boot.usb"
	R_PxeDisabled    RootCause = "seeded.boot.pxe.disabled"
	R_RaidMode       RootCause = "seeded.raid.mode"
	R_RaidConfig     RootCause = "seeded.raid.config"
	R_DimmShort      RootCause = "seeded.dimm.short"
	R_DimmWrongModel RootCause = "seeded.dimm.model"
	R_DiskWrongModel RootCause = "seeded.disk.model"
	R_DiskShort      RootCause = "seeded.disk.short"
	R_DiskBad        RootCause = "seeded.disk.bad"
	R_NicOldFw       RootCause = "seeded.nic.firmware"
	R_NicWrongModel  RootCause = "seeded.nic.model"
	R_MemEcc         RootCause = "seeded.mem.ecc"
	R_SwitchOff      RootCause = "seeded.switch.off"
	R_SwitchCap      RootCause = "seeded.switch.cap"
	R_ImageCorrupt   RootCause = "seeded.image.corrupt"
	R_ImageSlow      RootCause = "seeded.image.slow"
	R_InstallScript  RootCause = "seeded.install.script"
)

// RootCauseLabel renders a seeded defect for the ground-truth panel.
func RootCauseLabel(r RootCause) string {
	switch r {
	case R_None:
		return "正常"
	case R_BootOrder:
		return "启动顺序:本地盘优先"
	case R_UsbStick:
		return "启动顺序:外接设备优先"
	case R_PxeDisabled:
		return "网卡:网络启动禁用"
	case R_RaidMode:
		return "阵列控制器:AHCI模式"
	case R_RaidConfig:
		return "阵列配置:残留阵列"
	case R_DimmShort:
		return "内存:少插一根"
	case R_DimmWrongModel:
		return "内存:型号错误"
	case R_DiskWrongModel:
		return "硬盘:型号/容量错误"
	case R_DiskShort:
		return "硬盘:少一块"
	case R_DiskBad:
		return "硬盘:SMART故障"
	case R_NicOldFw:
		return "网卡:固件过旧"
	case R_NicWrongModel:
		return "网卡:型号错误"
	case R_MemEcc:
		return "内存:ECC错误"
	case R_SwitchOff:
		return "机柜交换机:未放行启动网络"
	case R_SwitchCap:
		return "机柜交换机:启动会话上限过低"
	case R_ImageCorrupt:
		return "镜像:文件校验不通过"
	case R_ImageSlow:
		return "镜像:服务端慢/丢包"
	case R_InstallScript:
		return "装机脚本:模板缺陷"
	}
	return string(r)
}

// SymptomGroup collapses faults that look identical in the protocol trace.
// The scorecard gives partial credit where the tool saw the right symptom
// but named a neighbouring root cause.
func SymptomGroup(c FaultCode) string {
	switch c {
	case F_BOOT_ORDER, F_BOOT_USB, F_BOOT_SILENT, F_PXE_DISABLED, F_NIC_FIRMWARE, F_NIC_MODEL:
		return "no-boot"
	case F_SWITCH_OFF, F_SWITCH_CAP, F_DHCP_TIMEOUT, F_DHCP_EXHAUST:
		return "dhcp"
	case F_TFTP_BUSY, F_TFTP_CORRUPT:
		return "tftp"
	case F_HTTP_BUSY, F_HTTP_TIMEOUT, F_IMAGE_CORRUPT:
		return "image"
	case F_DIMM_SEAT, F_DIMM_MODEL, F_MEM_ECC:
		return "memory"
	case F_DISK_MODEL, F_DISK_COUNT, F_DISK_BAD:
		return "disk"
	case F_RAID_MODE, F_RAID_CFG, F_TEMPLATE:
		return "raid"
	}
	return "other"
}
