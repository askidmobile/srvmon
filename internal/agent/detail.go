package agent

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// clkTck is USER_HZ, the unit of /proc/<pid>/stat times. It is 100 on every
// mainstream Linux build.
const clkTck = 100

// maxProcs is how many processes go into a detailed report.
const maxProcs = 40

// DetailCollector gathers the data for the server screen: per-core CPU,
// processes with their containers, disk IO, temperature. It keeps its own
// previous samples, so rates cover the time between two detailed reports.
type DetailCollector struct {
	HostProc string
	HostRoot string

	prevAt    time.Time
	prevCPU   map[string]cpuTimes // "cpu", "cpu0", ...
	prevProc  map[int]uint64      // pid -> utime+stime ticks
	prevDisk  [2]uint64           // read, written sectors
	users     map[int]string
	usersRead time.Time
}

type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (t cpuTimes) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

func (d *DetailCollector) proc(p string) string { return filepath.Join(d.HostProc, p) }

// Collect returns one detailed sample. containerIDs maps full container ids
// to names; per-container CPU and memory are returned keyed by name.
func (d *DetailCollector) Collect(containerIDs map[string]string) (*protocol.Detail, map[string]ContainerUsage) {
	now := time.Now()
	dt := now.Sub(d.prevAt).Seconds()
	if d.prevAt.IsZero() || dt <= 0 {
		dt = 0
	}
	det := &protocol.Detail{}
	d.cpu(det)
	d.diskIO(det, dt)
	det.TempC = d.temperature()
	det.TCPConns = d.tcpConns()
	usage := d.processes(det, dt, containerIDs)
	d.prevAt = now
	return det, usage
}

func (d *DetailCollector) cpu(det *protocol.Detail) {
	b, err := os.ReadFile(d.proc("stat"))
	if err != nil {
		return
	}
	cur := parseCPUTimes(b)
	pct := func(name string) (cpuTimes, float64, bool) {
		c, ok := cur[name]
		p, okp := d.prevCPU[name]
		if !ok || !okp || c.total() <= p.total() {
			return cpuTimes{}, 0, false
		}
		dt := float64(c.total() - p.total())
		delta := cpuTimes{
			user: c.user - p.user, nice: c.nice - p.nice, system: c.system - p.system,
			idle: c.idle - p.idle, iowait: c.iowait - p.iowait, irq: c.irq - p.irq,
			softirq: c.softirq - p.softirq, steal: c.steal - p.steal,
		}
		return delta, dt, true
	}
	if delta, dt, ok := pct("cpu"); ok {
		det.CPUUser = float64(delta.user+delta.nice) / dt * 100
		det.CPUSystem = float64(delta.system+delta.irq+delta.softirq) / dt * 100
		det.CPUIOWait = float64(delta.iowait) / dt * 100
		det.CPUSteal = float64(delta.steal) / dt * 100
	}
	for i := 0; ; i++ {
		name := "cpu" + strconv.Itoa(i)
		if _, ok := cur[name]; !ok {
			break
		}
		v := 0.0
		if delta, dt, ok := pct(name); ok {
			v = (1 - float64(delta.idle+delta.iowait)/dt) * 100
		}
		det.Cores = append(det.Cores, v)
	}
	d.prevCPU = cur
}

func parseCPUTimes(b []byte) map[string]cpuTimes {
	out := map[string]cpuTimes{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var v [8]uint64
		for i := 0; i < 8 && i+1 < len(f); i++ {
			v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
		}
		out[f[0]] = cpuTimes{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
	}
	return out
}

// partition matches partitions and virtual devices whose IO is already
// counted on the underlying disk.
var partition = regexp.MustCompile(`^((sd|vd|xvd|hd)[a-z]+\d+|nvme\d+n\d+p\d+|mmcblk\d+p\d+|loop\d+|ram\d+|zram\d+|sr\d+|dm-\d+|md\d+)$`)

func (d *DetailCollector) diskIO(det *protocol.Detail, dt float64) {
	b, err := os.ReadFile(d.proc("diskstats"))
	if err != nil {
		return
	}
	rd, wr := parseDiskstats(b)
	if dt > 0 && rd >= d.prevDisk[0] && wr >= d.prevDisk[1] {
		det.DiskReadBps = float64(rd-d.prevDisk[0]) * 512 / dt
		det.DiskWriteBps = float64(wr-d.prevDisk[1]) * 512 / dt
	}
	d.prevDisk = [2]uint64{rd, wr}
}

// parseDiskstats sums sectors read and written over whole disks.
func parseDiskstats(b []byte) (read, written uint64) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 || partition.MatchString(f[2]) {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		read += r
		written += w
	}
	return read, written
}

func (d *DetailCollector) tcpConns() int {
	b, err := os.ReadFile(d.proc("net/sockstat"))
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 3 && f[0] == "TCP:" && f[1] == "inuse" {
			n, _ := strconv.Atoi(f[2])
			return n
		}
	}
	return 0
}

// cpuSensors are hwmon drivers that report the CPU package temperature.
var cpuSensors = []string{"k10temp", "coretemp", "zenpower", "cpu_thermal", "soc_thermal"}

func (d *DetailCollector) temperature() *float64 {
	sys := filepath.Join(d.HostRoot, "sys")
	dirs, _ := filepath.Glob(filepath.Join(sys, "class/hwmon/hwmon*"))
	found := map[string]string{}
	for _, dir := range dirs {
		name, err := os.ReadFile(filepath.Join(dir, "name"))
		if err != nil {
			continue
		}
		found[strings.TrimSpace(string(name))] = dir
	}
	for _, s := range cpuSensors {
		if dir, ok := found[s]; ok {
			if t, ok := readMilliC(filepath.Join(dir, "temp1_input")); ok {
				return &t
			}
		}
	}
	if t, ok := readMilliC(filepath.Join(sys, "class/thermal/thermal_zone0/temp")); ok {
		return &t
	}
	return nil
}

func readMilliC(p string) (float64, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n / 1000, true
}

// ContainerUsage is CPU and memory summed over a container's processes.
type ContainerUsage struct {
	CPUPercent float64
	MemBytes   uint64
}

type procSample struct {
	pid       int
	name      string
	uid       int
	ticks     uint64
	rss       uint64
	cpu       float64
	container string
}

func (d *DetailCollector) processes(det *protocol.Detail, dt float64, containerIDs map[string]string) map[string]ContainerUsage {
	entries, err := os.ReadDir(d.HostProc)
	if err != nil {
		return nil
	}
	page := uint64(os.Getpagesize())
	cur := make(map[int]uint64, len(entries))
	var list []procSample
	usage := map[string]ContainerUsage{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(d.proc(e.Name() + "/stat"))
		if err != nil {
			continue
		}
		name, ticks, rssPages, ok := parsePidStat(b)
		if !ok {
			continue
		}
		p := procSample{pid: pid, name: name, ticks: ticks, rss: rssPages * page, uid: -1}
		cur[pid] = ticks
		if prev, ok := d.prevProc[pid]; ok && dt > 0 && ticks >= prev {
			p.cpu = float64(ticks-prev) / clkTck / dt * 100
		}
		if len(containerIDs) > 0 {
			if cg, err := os.ReadFile(d.proc(e.Name() + "/cgroup")); err == nil {
				if id := containerIDFromCgroup(cg); id != "" {
					if n, ok := containerIDs[id]; ok {
						p.container = n
						u := usage[n]
						u.CPUPercent += p.cpu
						u.MemBytes += p.rss
						usage[n] = u
					}
				}
			}
		}
		list = append(list, p)
	}
	d.prevProc = cur
	det.ProcsTotal = len(list)
	sort.Slice(list, func(i, j int) bool {
		if list[i].cpu != list[j].cpu {
			return list[i].cpu > list[j].cpu
		}
		return list[i].rss > list[j].rss
	})
	if len(list) > maxProcs {
		list = list[:maxProcs]
	}
	users := d.userNames()
	for _, p := range list {
		out := protocol.Process{
			PID: p.pid, Name: p.name, CPUPercent: p.cpu, MemBytes: p.rss, Container: p.container,
			Cmd: d.cmdline(p.pid),
		}
		if uid := d.uid(p.pid); uid >= 0 {
			if n, ok := users[uid]; ok {
				out.User = n
			} else {
				out.User = strconv.Itoa(uid)
			}
		}
		det.Procs = append(det.Procs, out)
	}
	return usage
}

// parsePidStat extracts comm, utime+stime and rss pages from /proc/<pid>/stat.
// comm may contain spaces and parentheses, so fields are counted after the
// last ')'.
func parsePidStat(b []byte) (name string, ticks, rss uint64, ok bool) {
	s := string(b)
	open := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return "", 0, 0, false
	}
	name = s[open+1 : end]
	f := strings.Fields(s[end+1:])
	// f[0] is state (field 3); utime is field 14, stime 15, rss 24.
	if len(f) < 22 {
		return "", 0, 0, false
	}
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ = strconv.ParseUint(f[21], 10, 64)
	return name, ut + st, rss, true
}

var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// containerIDFromCgroup finds a docker container id in /proc/<pid>/cgroup
// (cgroup v1 ".../docker/<id>" and v2 ".../docker-<id>.scope").
func containerIDFromCgroup(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "docker") && !strings.Contains(line, "containerd") {
			continue
		}
		if id := containerIDRe.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

func (d *DetailCollector) cmdline(pid int) string {
	b, err := os.ReadFile(d.proc(strconv.Itoa(pid) + "/cmdline"))
	if err != nil {
		return ""
	}
	if len(b) > 300 {
		b = b[:300]
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}

func (d *DetailCollector) uid(pid int) int {
	b, err := os.ReadFile(d.proc(strconv.Itoa(pid) + "/status"))
	if err != nil {
		return -1
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				n, err := strconv.Atoi(f[0])
				if err == nil {
					return n
				}
			}
		}
	}
	return -1
}

// userNames reads the host's /etc/passwd at most once a minute.
func (d *DetailCollector) userNames() map[int]string {
	if d.users != nil && time.Since(d.usersRead) < time.Minute {
		return d.users
	}
	d.users = map[int]string{}
	d.usersRead = time.Now()
	b, err := os.ReadFile(filepath.Join(d.HostRoot, "etc/passwd"))
	if err != nil {
		return d.users
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil {
			d.users[uid] = f[0]
		}
	}
	return d.users
}
