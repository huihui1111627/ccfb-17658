package domain

import "sort"

// Stage is one step in the simulated bare-metal provisioning state machine.
type Stage string

const (
	StageQueued        Stage = "queued"
	StagePowerOn       Stage = "power_on"
	StagePOST          Stage = "post"
	StageBootOrder     Stage = "boot_order"
	StageNICFirmware   Stage = "nic_firmware"
	StageDHCP          Stage = "dhcp"
	StageTFTPBoot      Stage = "tftp_boot"
	StageHTTPBoot      Stage = "http_boot"
	StageInventory     Stage = "inventory"
	StageRAIDPlan      Stage = "raid_plan"
	StagePartition     Stage = "partition"
	StageImageDownload Stage = "image_download"
	StageInstallOS     Stage = "install_os"
	StageReboot        Stage = "reboot"
	StageFinalBoot     Stage = "final_boot"
	StageHealthCheck   Stage = "health_check"
	StageDone          Stage = "done"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusRetrying Status = "retrying"
	StatusBlocked  Status = "blocked"
	StatusFailed   Status = "failed"
	StatusSuccess  Status = "success"
)

// Outcome groups terminal states for rollout reporting.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeBlocked Outcome = "blocked"
	OutcomeFailed  Outcome = "failed"
	OutcomeActive  Outcome = "active"
)

type Issue struct {
	Code     string            `json:"code"`
	Category string            `json:"category"`
	Message  string            `json:"message"`
	Evidence map[string]string `json:"evidence,omitempty"`
}

type Attribution struct {
	Code       string   `json:"code"`
	Category   string   `json:"category"`
	RootCause  string   `json:"rootCause"`
	Confidence float64  `json:"confidence"`
	Scope      string   `json:"scope"`
	RelatedID  string   `json:"relatedId,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
}

type LogEntry struct {
	TimeMS    int64  `json:"t"`
	Stage     Stage  `json:"stage"`
	Protocol  string `json:"protocol"`
	Direction string `json:"direction"`
	Endpoint  string `json:"endpoint"`
	Message   string `json:"message"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs,omitempty"`
}

type DIMM struct {
	Slot    string `json:"slot"`
	Model   string `json:"model"`
	SizeGB  int    `json:"sizeGb"`
	SpeedMT int    `json:"speedMt"`
}

type Disk struct {
	Slot     string `json:"slot"`
	Model    string `json:"model"`
	Vendor   string `json:"vendor"`
	Serial   string `json:"serial"`
	SizeGB   int    `json:"sizeGb"`
	Type     string `json:"type"`
	Firmware string `json:"firmware"`
}

type NIC struct {
	Name          string `json:"name"`
	Model         string `json:"model"`
	Firmware      string `json:"firmware"`
	MinFirmware   string `json:"minFirmware"`
	PXEROM        string `json:"pxeRom"`
	LinkSpeedGbps int    `json:"linkSpeedGbps"`
	MAC           string `json:"mac"`
}

type RAIDVolume struct {
	Name      string   `json:"name"`
	Level     string   `json:"level"`
	DiskSlots []string `json:"diskSlots"`
	SizeGB    int      `json:"sizeGb"`
}

type RAIDState struct {
	Controller      string       `json:"controller"`
	Firmware        string       `json:"firmware"`
	Personality     string       `json:"personality"` // RAID or JBOD/HBA
	CachePolicy     string       `json:"cachePolicy"`
	BatteryOK       bool         `json:"batteryOk"`
	ExistingVolumes []RAIDVolume `json:"existingVolumes"`
}

type Hardware struct {
	Vendor      string    `json:"vendor"`
	Model       string    `json:"model"`
	Serial      string    `json:"serial"`
	BIOSVersion string    `json:"biosVersion"`
	CPUModel    string    `json:"cpuModel"`
	CPUSockets  int       `json:"cpuSockets"`
	MemoryGB    int       `json:"memoryGb"`
	DIMMs       []DIMM    `json:"dimms"`
	Disks       []Disk    `json:"disks"`
	NICs        []NIC     `json:"nics"`
	RAID        RAIDState `json:"raid"`
}

type PurchaseSpec struct {
	OrderID          string   `json:"orderId"`
	SKU              string   `json:"sku"`
	Model            string   `json:"model"`
	Role             string   `json:"role"`
	Template         string   `json:"template"`
	ExpectedMemoryGB int      `json:"expectedMemoryGb"`
	DIMMCount        int      `json:"dimmCount"`
	DIMMModel        string   `json:"dimmModel"`
	DiskCount        int      `json:"diskCount"`
	DiskModel        string   `json:"diskModel"`
	NICModel         string   `json:"nicModel"`
	MinNICFirmware   string   `json:"minNicFirmware"`
	Expected         Hardware `json:"expected"`
}

type XferState struct {
	Service     string  `json:"service"`
	File        string  `json:"file"`
	TotalBytes  int64   `json:"totalBytes"`
	DoneBytes   int64   `json:"doneBytes"`
	SpeedBps    float64 `json:"speedBps"`
	Efficiency  float64 `json:"efficiency"`
	StartedAtMS int64   `json:"startedAtMs"`
	TimeoutAtMS int64   `json:"timeoutAtMs"`
}

type Machine struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Serial          string       `json:"serial"`
	RackID          string       `json:"rackId"`
	RackIndex       int          `json:"rackIndex"`
	UPosition       int          `json:"uPosition"`
	SwitchPort      string       `json:"switchPort"`
	ModelID         string       `json:"modelId"`
	TemplateID      string       `json:"templateId"`
	MAC             string       `json:"mac"`
	BMCIP           string       `json:"bmcIp"`
	AssignedIP      string       `json:"assignedIp,omitempty"`
	Batch           int          `json:"batch"`
	BootOrder       []string     `json:"bootOrder"`
	TransferQuality float64      `json:"transferQuality"`
	Hardware        Hardware     `json:"hardware"`
	Purchase        PurchaseSpec `json:"purchase"`
	Truth           *Issue       `json:"truth,omitempty"`
	Attribution     *Attribution `json:"attribution,omitempty"`

	Stage            Stage          `json:"stage"`
	Status           Status         `json:"status"`
	Progress         float64        `json:"progress"`
	PowerAtMS        int64          `json:"powerAtMs"`
	StartedAtMS      int64          `json:"startedAtMs"`
	StageAtMS        int64          `json:"stageAtMs"`
	UpdatedAtMS      int64          `json:"updatedAtMs"`
	FinishedAtMS     int64          `json:"finishedAtMs,omitempty"`
	NextCheckAtMS    int64          `json:"-"`
	TimeoutAtMS      int64          `json:"-"`
	HardDeadlineAtMS int64          `json:"-"`
	ResumeBytes      int64          `json:"-"`
	NextStage        Stage          `json:"-"`
	PendingAction    string         `json:"-"`
	Attempts         map[string]int `json:"attempts"`
	Xfer             *XferState     `json:"xfer,omitempty"`
	LastError        string         `json:"lastError,omitempty"`
	Logs             []LogEntry     `json:"-"`
}

type RackSwitch struct {
	ID            string `json:"id"`
	Model         string `json:"model"`
	RackID        string `json:"rackId"`
	UplinkGbps    int    `json:"uplinkGbps"`
	ProvisionVLAN int    `json:"provisionVlan"`
	VLANs         []int  `json:"vlans"`
	MissingVLAN   int    `json:"missingVlan,omitempty"`
	ConfigManaged bool   `json:"configManaged"`
}

type Rack struct {
	ID          string     `json:"id"`
	Index       int        `json:"index"`
	Switch      RackSwitch `json:"switch"`
	PowerFeedKW float64    `json:"powerFeedKw"`
}

type PartitionSpec struct {
	Mount      string `json:"mount"`
	SizeMB     int    `json:"sizeMb"` // 0 means remainder
	FileSystem string `json:"fs"`
}

type RAIDVolumeTemplate struct {
	Name      string `json:"name"`
	Level     string `json:"level"`
	DiskCount int    `json:"diskCount"`
	HotSpare  int    `json:"hotSpare"`
}

type RAIDTemplate struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	ApplicableModels []string             `json:"applicableModels"`
	MinDisks         int                  `json:"minDisks"`
	RequiredDiskType string               `json:"requiredDiskType,omitempty"`
	RequiredModel    string               `json:"requiredModel,omitempty"`
	Controller       string               `json:"controller"`
	Personality      string               `json:"personality"`
	Volumes          []RAIDVolumeTemplate `json:"volumes"`
	Partitions       []PartitionSpec      `json:"partitions"`
}

type Fleet struct {
	Seed      int64                   `json:"seed"`
	Total     int                     `json:"total"`
	Machines  []*Machine              `json:"machines"`
	Racks     []Rack                  `json:"racks"`
	Templates map[string]RAIDTemplate `json:"templates"`
}

func (f *Fleet) MachineByID(id string) *Machine {
	for _, m := range f.Machines {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func SortedStages() []Stage {
	return []Stage{
		StagePowerOn, StagePOST, StageBootOrder, StageNICFirmware,
		StageDHCP, StageTFTPBoot, StageHTTPBoot, StageInventory,
		StageRAIDPlan, StagePartition, StageImageDownload, StageInstallOS,
		StageReboot, StageFinalBoot, StageHealthCheck, StageDone,
	}
}

var StageLabels = map[Stage]string{
	StageQueued:        "排队等待上电",
	StagePowerOn:       "上电/BMC",
	StagePOST:          "BIOS POST",
	StageBootOrder:     "启动顺序",
	StageNICFirmware:   "网卡固件/PXE ROM",
	StageDHCP:          "DHCP 获取地址",
	StageTFTPBoot:      "TFTP 下载引导",
	StageHTTPBoot:      "HTTP 拉取内核/Initrd",
	StageInventory:     "硬件清点核对",
	StageRAIDPlan:      "RAID/阵列模板校验",
	StagePartition:     "分区/格式化",
	StageImageDownload: "下载安装镜像",
	StageInstallOS:     "释放镜像/安装系统",
	StageReboot:        "重启",
	StageFinalBoot:     "硬盘启动新系统",
	StageHealthCheck:   "Agent 健康检查",
	StageDone:          "装机完成",
}

func StageOrderIndex(s Stage) int {
	stages := SortedStages()
	for i, candidate := range stages {
		if candidate == s {
			return i
		}
	}
	return -1
}

func SortLogs(logs []LogEntry) {
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].TimeMS == logs[j].TimeMS {
			return i < j
		}
		return logs[i].TimeMS < logs[j].TimeMS
	})
}
