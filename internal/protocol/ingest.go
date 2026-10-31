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
	// Detail is sent only while someone has the server screen open.
	Detail *Detail `json:"detail,omitempty"`
	// Inventory is sent with the first report, then about once a minute.
	Inventory *Inventory `json:"inventory,omitempty"`
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
	MemFree      uint64     `json:"mem_free,omitempty"`
	MemCached    uint64     `json:"mem_cached,omitempty"` // page cache + reclaimable slab
	MemBuffers   uint64     `json:"mem_buffers,omitempty"`
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
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Image  string   `json:"image"`
	State  string   `json:"state"`  // running, exited, restarting, ...
	Status string   `json:"status"` // human readable, e.g. "Up 3 hours (healthy)"
	Health string   `json:"health,omitempty"`
	Ports  []string `json:"ports,omitempty"` // "8080→80/tcp", "6379/tcp"
	// Compose is the docker compose project label, if any.
	Compose string `json:"compose,omitempty"`
	// Detail-only fields: restart count from inspect and usage summed over
	// the container's processes.
	Restarts   *int     `json:"restarts,omitempty"`
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
	MemBytes   *uint64  `json:"mem_bytes,omitempty"`
}

// Detail is the extra data for the btop-like server screen.
type Detail struct {
	// CPU split, percent of all cores.
	CPUUser   float64   `json:"cpu_user"`
	CPUSystem float64   `json:"cpu_system"`
	CPUIOWait float64   `json:"cpu_iowait"`
	CPUSteal  float64   `json:"cpu_steal"`
	Cores     []float64 `json:"cores"` // percent per logical core
	TempC     *float64  `json:"temp_c,omitempty"`
	// Disk IO of whole block devices, bytes per second.
	DiskReadBps  float64   `json:"disk_read_bps"`
	DiskWriteBps float64   `json:"disk_write_bps"`
	TCPConns     int       `json:"tcp_conns"`
	ProcsTotal   int       `json:"procs_total"`
	Procs        []Process `json:"procs"` // top by CPU, then memory
	// Logs are new warn+ lines since the previous report.
	Logs []LogLine `json:"logs,omitempty"`
}

// Process is one entry of the top list.
type Process struct {
	PID        int     `json:"pid"`
	Name       string  `json:"name"`
	Cmd        string  `json:"cmd,omitempty"`
	User       string  `json:"user,omitempty"`
	CPUPercent float64 `json:"cpu"` // percent of one core, like top
	MemBytes   uint64  `json:"mem"`
	Container  string  `json:"container,omitempty"`
}

// LogLine is one warn-or-worse line from syslog or a container.
type LogLine struct {
	Time   time.Time `json:"ts"`
	Level  string    `json:"level"` // ERR, WARN
	Source string    `json:"source"`
	Msg    string    `json:"msg"`
}

// Inventory is slowly changing host information for the passport.
type Inventory struct {
	OS            string   `json:"os,omitempty"`
	Kernel        string   `json:"kernel,omitempty"`
	CPUModel      string   `json:"cpu_model,omitempty"`
	DockerVersion string   `json:"docker_version,omitempty"`
	IPs           []string `json:"ips,omitempty"`
}

// IngestResponse is returned to the agent.
type IngestResponse struct {
	// Detail asks the agent to send detailed data (processes, per-core CPU,
	// logs) because someone has the server screen open.
	Detail bool `json:"detail"`
}
