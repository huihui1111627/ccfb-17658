package model

// Pools are the finite resources shared by all machines.
type Pools struct {
	DHCPLeases     int `json:"dhcpLeases"`
	DHCPSeats      int `json:"dhcpSeats"`
	DHCPSeatMs     int `json:"dhcpSeatMs"` // handling time per request, ms
	TFTPStreams    int `json:"tftpStreams"`
	TFTPMBps       int `json:"tftpMBps"` // per stream
	BootloaderMB   int `json:"bootloaderMB"`
	HTTPStreams    int `json:"httpStreams"`
	HTTPMBps       int `json:"httpMBps"`       // aggregate
	HTTPStreamMBps int `json:"httpStreamMBps"` // per stream ceiling
	InstallShare   int `json:"installShare"`   // concurrent install slots
}

// ServiceSnapshot is a telemetry sample of one shared pool.
type ServiceSnapshot struct {
	T       float64 `json:"t"`
	Pool    string  `json:"pool"` // dhcp | tftp | http | install
	InUse   int     `json:"inUse"`
	Queue   int     `json:"queue"`
	Served  int     `json:"served"`
	Refused int     `json:"refused"`
	Cap     int     `json:"cap"`
}
