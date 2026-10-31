// Package protocol describes the data the agent pushes to the central server.
package protocol

import "time"

// Report is one push from srvmon-agent to POST /api/ingest.
type Report struct {
	Hostname     string      `json:"hostname"`
	AgentVersion string      `json:"agent_version"`
	Timestamp    time.Time   `json:"ts"`
	Host         HostMetrics `json:"host"`
	// Containers is nil when the docker socket is not available.
	Containers []Container `json:"containers,omitempty"`
	DockerOK   bool        `json:"docker_ok"`
}

// HostMetrics are the basic host-level metrics collected every interval.
type HostMetrics struct {
	CPUPercent   float64    `json:"cpu_percent"`
	CPUCores     int        `json:"cpu_cores"`
	Load1        float64    `json:"load1"`
	Load5        float64    `json:"load5"`
	Load15       float64    `json:"load15"`
	MemTotal     uint64     `json:"mem_total"`
	MemAvailable uint64     `json:"mem_available"`
	SwapTotal    uint64     `json:"swap_total"`
	SwapFree     uint64     `json:"swap_free"`
	UptimeSec    float64    `json:"uptime_sec"`
	Disks        []Disk     `json:"disks,omitempty"`
	Net          []NetIface `json:"net,omitempty"`
}

// MemUsed returns used memory in bytes.
func (h HostMetrics) MemUsed() uint64 {
	if h.MemAvailable > h.MemTotal {
		return 0
	}
	return h.MemTotal - h.MemAvailable
}

// Disk is usage of one mounted filesystem.
type Disk struct {
	Mount string `json:"mount"`
	FS    string `json:"fs"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// NetIface holds cumulative byte counters of a network interface.
type NetIface struct {
	Name    string `json:"name"`
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

// Container is the state of one docker container.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`  // running, exited, restarting, ...
	Status string `json:"status"` // human readable, e.g. "Up 3 hours (healthy)"
	Health string `json:"health,omitempty"`
}

// IngestResponse is returned to the agent.
type IngestResponse struct {
	// Detail asks the agent to send detailed data (processes, per-core CPU)
	// because someone has the server screen open. Reserved for stage 2.
	Detail bool `json:"detail"`
}
