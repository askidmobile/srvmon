// Package agent collects host and docker metrics and pushes them to srvmon-server.
package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// Collector reads host metrics from procfs. Inside a container the host's
// /proc and / are mounted read-only at HostProc and HostRoot.
type Collector struct {
	HostProc string // default "/proc"
	HostRoot string // default "/"

	prevTotal, prevIdle uint64
}

func (c *Collector) proc(p string) string { return filepath.Join(c.HostProc, p) }

// Collect gathers one sample of host metrics.
func (c *Collector) Collect() (protocol.HostMetrics, error) {
	var h protocol.HostMetrics
	if err := c.cpu(&h); err != nil {
		return h, fmt.Errorf("cpu: %w", err)
	}
	if err := c.mem(&h); err != nil {
		return h, fmt.Errorf("mem: %w", err)
	}
	c.load(&h)
	c.uptime(&h)
	h.Disks = c.disks()
	h.Net = c.net()
	return h, nil
}

func (c *Collector) cpu(h *protocol.HostMetrics) error {
	b, err := os.ReadFile(c.proc("stat"))
	if err != nil {
		return err
	}
	total, idle, cores, err := parseStat(b)
	if err != nil {
		return err
	}
	h.CPUCores = cores
	if c.prevTotal > 0 && total > c.prevTotal {
		dt := float64(total - c.prevTotal)
		di := float64(idle - c.prevIdle)
		h.CPUPercent = (1 - di/dt) * 100
	}
	c.prevTotal, c.prevIdle = total, idle
	return nil
}

// parseStat returns aggregate jiffies, idle (+iowait) jiffies and core count.
func parseStat(b []byte) (total, idle uint64, cores int, err error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			if len(f) < 5 {
				return 0, 0, 0, fmt.Errorf("short cpu line")
			}
			for i, v := range f[1:] {
				if i >= 8 { // guest and guest_nice are already part of user/nice
					break
				}
				n, _ := strconv.ParseUint(v, 10, 64)
				total += n
				if i == 3 || i == 4 { // idle, iowait
					idle += n
				}
			}
		case strings.HasPrefix(f[0], "cpu"):
			cores++
		}
	}
	if total == 0 {
		return 0, 0, 0, fmt.Errorf("no cpu line")
	}
	return total, idle, cores, sc.Err()
}

func (c *Collector) mem(h *protocol.HostMetrics) error {
	b, err := os.ReadFile(c.proc("meminfo"))
	if err != nil {
		return err
	}
	m := parseMeminfo(b)
	h.MemTotal = m["MemTotal"]
	h.MemAvailable = m["MemAvailable"]
	h.SwapTotal = m["SwapTotal"]
	h.SwapFree = m["SwapFree"]
	h.MemFree = m["MemFree"]
	h.MemCached = m["Cached"] + m["SReclaimable"]
	h.MemBuffers = m["Buffers"]
	return nil
}

// parseMeminfo returns values in bytes.
func parseMeminfo(b []byte) map[string]uint64 {
	out := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[key] = n
	}
	return out
}

func (c *Collector) load(h *protocol.HostMetrics) {
	b, err := os.ReadFile(c.proc("loadavg"))
	if err != nil {
		return
	}
	f := strings.Fields(string(b))
	if len(f) >= 3 {
		h.Load1, _ = strconv.ParseFloat(f[0], 64)
		h.Load5, _ = strconv.ParseFloat(f[1], 64)
		h.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
}

func (c *Collector) uptime(h *protocol.HostMetrics) {
	b, err := os.ReadFile(c.proc("uptime"))
	if err != nil {
		return
	}
	if f := strings.Fields(string(b)); len(f) > 0 {
		h.UptimeSec, _ = strconv.ParseFloat(f[0], 64)
	}
}

func (c *Collector) net() []protocol.NetIface {
	b, err := os.ReadFile(c.proc("net/dev"))
	if err != nil {
		return nil
	}
	return parseNetDev(b)
}

func parseNetDev(b []byte) []protocol.NetIface {
	var out []protocol.NetIface
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") || name == "docker0" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		out = append(out, protocol.NetIface{Name: name, RxBytes: rx, TxBytes: tx})
	}
	return out
}

// realFS lists filesystems worth reporting; tmpfs, overlay and friends are skipped.
var realFS = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"vfat": true, "f2fs": true, "jfs": true, "reiserfs": true, "bcachefs": true,
}

func (c *Collector) disks() []protocol.Disk {
	b, err := os.ReadFile(c.proc("1/mounts"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []protocol.Disk
	for _, m := range parseMounts(b) {
		if !realFS[m.fs] || seen[m.dev] {
			continue
		}
		seen[m.dev] = true
		total, used, err := statfs(filepath.Join(c.HostRoot, m.mount))
		if err != nil || total == 0 {
			continue
		}
		out = append(out, protocol.Disk{Mount: m.mount, FS: m.fs, Total: total, Used: used})
	}
	return out
}

type mount struct{ dev, mount, fs string }

func parseMounts(b []byte) []mount {
	var out []mount
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		// Mount points escape spaces as \040.
		out = append(out, mount{dev: f[0], mount: strings.ReplaceAll(f[1], `\040`, " "), fs: f[2]})
	}
	return out
}
