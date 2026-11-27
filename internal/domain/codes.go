package domain

// Root-cause / diagnosis codes. The same codes are used for hidden ground
// truth and attributed causes so per-machine verification is exact.
const (
	CodeHealthy                = "HEALTHY"
	CodeBootOrderWrong         = "BOOT_ORDER_WRONG"
	CodeNICFirmwareUnsupported = "NIC_FIRMWARE_UNSUPPORTED"
	CodeSwitchVLANMissing      = "SWITCH_VLAN_MISSING"
	CodeMemoryDIMMMissing      = "MEMORY_DIMM_MISSING"
	CodeDiskModelMismatch      = "DISK_MODEL_MISMATCH"
	CodeRAIDModeMismatch       = "RAID_MODE_MISMATCH"
	CodeTemplateNotApplicable  = "TEMPLATE_NOT_APPLICABLE"
	CodeDHCPOverload           = "DHCP_SERVICE_OVERLOAD"
	CodeTFTPOverload           = "TFTP_SERVICE_OVERLOAD"
	CodeHTTPOverload           = "HTTP_SERVICE_OVERLOAD"
	CodeImageOverload          = "IMAGE_SERVICE_OVERLOAD"
	CodeUnknown                = "UNKNOWN"
)

var CodeLabels = map[string]string{
	CodeHealthy:                "正常",
	CodeBootOrderWrong:         "启动顺序/PXE 配置错误",
	CodeNICFirmwareUnsupported: "网卡固件或 PXE ROM 过旧",
	CodeSwitchVLANMissing:      "机柜交换机未放通装机 VLAN",
	CodeMemoryDIMMMissing:      "内存 DIMM 数量/容量不符",
	CodeDiskModelMismatch:      "硬盘型号或数量不符",
	CodeRAIDModeMismatch:       "RAID 控制器模式/阵列配置错误",
	CodeTemplateNotApplicable:  "RAID/分区模板不适用于该机型",
	CodeDHCPOverload:           "DHCP/地址分配服务拥塞",
	CodeTFTPOverload:           "TFTP/引导文件服务拥塞",
	CodeHTTPOverload:           "HTTP 引导文件服务拥塞",
	CodeImageOverload:          "镜像服务或网络拥塞",
	CodeUnknown:                "未归类原因",
}
