package model

// Event is one line of a machine's interaction record — what the console or
// the BMC's serial-over-LAN would show, plus the tool's own annotations.
type Event struct {
	TimeS    float64   `json:"t"`
	Proto    string    `json:"proto"` // fw | bios | switch | dhcp | tftp | http | agent | kernel | net
	Line     string    `json:"line"`
	Severity string    `json:"severity,omitempty"` // info | warn | error
	Fault    FaultCode `json:"fault,omitempty"`
}

// Transition is a compact phase-change record. The UI reconstructs the wall
// at any scrub time from these instead of storing a snapshot per tick.
type Transition struct {
	T       float64 `json:"t"`
	Phase   Phase   `json:"phase"`
	Detail  string  `json:"detail,omitempty"`
	Attempt int     `json:"attempt,omitempty"`
}
