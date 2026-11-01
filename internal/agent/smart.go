package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// SmartEvery is how often an agent with S.M.A.R.T. enabled reads its disks.
const SmartEvery = 30 * time.Minute

// Errors of a disk read, sent to the panel as DiskHealth.Error.
var (
	errSmartStandby     = errors.New("standby")
	errSmartUnsupported = errors.New("unsupported")
	errSmartPermission  = errors.New("permission")
)

// smartLoop reads S.M.A.R.T. of all disks every SmartEvery. Reads run in
// their own goroutine (a slow disk may take seconds per command), so they
// never hold up the regular reports; a finished read rides the next one.
type smartLoop struct {
	enabled  bool
	hostRoot string
	log      *slog.Logger
	read     func(hostRoot string) []protocol.DiskHealth
	at       time.Time // start of the last read
	running  bool
	done     chan []protocol.DiskHealth
	warned   bool
}

func newSmartLoop(enabled bool, hostRoot string, log *slog.Logger) *smartLoop {
	return &smartLoop{enabled: enabled, hostRoot: hostRoot, log: log, read: ReadSmart,
		done: make(chan []protocol.DiskHealth, 1)}
}

// collect starts a read when one is due and returns the result of a read
// that has finished since the last call, nil otherwise.
func (s *smartLoop) collect(ctx context.Context, now time.Time) []protocol.DiskHealth {
	if !s.enabled {
		return nil
	}
	select {
	case out := <-s.done:
		s.running = false
		s.checkAccess(out)
		return out
	default:
	}
	if !s.running && now.Sub(s.at) >= SmartEvery && ctx.Err() == nil {
		s.at, s.running = now, true
		go func() { s.done <- s.read(s.hostRoot) }()
	}
	return nil
}

func (s *smartLoop) checkAccess(out []protocol.DiskHealth) {
	denied := len(out) > 0
	for _, d := range out {
		if d.Error != errSmartPermission.Error() {
			denied = false
		}
	}
	if denied && !s.warned {
		s.log.Warn("S.M.A.R.T.: no access to disks; the agent container needs --cap-add SYS_RAWIO --cap-add SYS_ADMIN and read access to block devices")
		s.warned = true
	}
}

// request makes the next report start a read, unless one started in the
// last few minutes (the panel asks after its own restart).
func (s *smartLoop) request(now time.Time) {
	if s.enabled && !s.running && now.Sub(s.at) > smartMinGap {
		s.at = time.Time{}
	}
}

const smartMinGap = 5 * time.Minute

// diskNameRe matches whole physical disks in /sys/block: SCSI/SATA disks
// and NVMe namespaces (not partitions, not nvme multipath paths).
var diskNameRe = regexp.MustCompile(`^(sd[a-z]+|nvme\d+n\d+)$`)

// ReadSmart reads every physical disk listed in HostRoot/sys/block.
func ReadSmart(hostRoot string) []protocol.DiskHealth {
	entries, err := os.ReadDir(filepath.Join(hostRoot, "sys/block"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if diskNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]protocol.DiskHealth, 0, len(names))
	for _, name := range names {
		sys := filepath.Join(hostRoot, "sys/block", name, "device")
		if strings.HasPrefix(name, "sd") && readTrim(filepath.Join(hostRoot, "sys/block", name, "removable")) == "1" {
			continue
		}
		d := protocol.DiskHealth{Device: name}
		dev := filepath.Join(hostRoot, "dev", name)
		var err error
		if strings.HasPrefix(name, "nvme") {
			d.Type = "nvme"
			d.Model = readTrim(filepath.Join(sys, "model"))
			d.Serial = readTrim(filepath.Join(sys, "serial"))
			var log []byte
			if log, err = nvmeSmartLog(dev); err == nil {
				parseNVMeLog(log, &d)
			}
		} else {
			d.Type = "ata"
			d.Model = strings.TrimSpace(readTrim(filepath.Join(sys, "vendor")) + " " + readTrim(filepath.Join(sys, "model")))
			if strings.HasPrefix(d.Model, "ATA ") {
				d.Model = strings.TrimPrefix(d.Model, "ATA ")
			}
			var data []byte
			var passed *bool
			if data, passed, err = ataSmart(dev); err == nil {
				d.Passed = passed
				parseATAData(data, &d)
			}
		}
		if err != nil {
			d.Error = smartErrCode(err)
		}
		out = append(out, d)
	}
	return out
}

func smartErrCode(err error) string {
	switch {
	case errors.Is(err, errSmartStandby):
		return errSmartStandby.Error()
	case errors.Is(err, os.ErrPermission), errors.Is(err, errSmartPermission):
		return errSmartPermission.Error()
	}
	return errSmartUnsupported.Error()
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// parseNVMeLog reads the SMART / Health Information log page (02h).
func parseNVMeLog(b []byte, d *protocol.DiskHealth) {
	if len(b) < 192 {
		return
	}
	d.CriticalWarning = b[0]
	ok := b[0] == 0
	d.Passed = &ok
	if k := binary.LittleEndian.Uint16(b[1:3]); k > 0 {
		t := float64(k) - 273
		d.TempC = &t
	}
	u64 := func(off int) *uint64 {
		// 128-bit counters; the high half is zero for any real disk.
		v := binary.LittleEndian.Uint64(b[off : off+8])
		return &v
	}
	u8 := func(off int) *uint64 { v := uint64(b[off]); return &v }
	d.AvailableSpare = u8(3)
	d.SpareThreshold = u8(4)
	d.PercentUsed = u8(5)
	d.PowerOnHours = u64(128)
	d.MediaErrors = u64(160)
}

// parseATAData reads the attribute table of SMART READ DATA.
func parseATAData(b []byte, d *protocol.DiskHealth) {
	if len(b) < 362 {
		return
	}
	for i := 0; i < 30; i++ {
		a := b[2+i*12 : 2+(i+1)*12]
		id := a[0]
		if id == 0 {
			continue
		}
		raw := a[5:11]
		raw48 := uint64(raw[0]) | uint64(raw[1])<<8 | uint64(raw[2])<<16 | uint64(raw[3])<<24 |
			uint64(raw[4])<<32 | uint64(raw[5])<<40
		raw32 := raw48 & 0xffffffff
		switch id {
		case 5:
			d.Reallocated = &raw48
		case 9:
			v := raw32
			d.PowerOnHours = &v
		case 194:
			t := float64(raw[0])
			d.TempC = &t
		case 190:
			if d.TempC == nil {
				t := float64(raw[0])
				d.TempC = &t
			}
		case 197:
			v := raw32
			d.Pending = &v
		case 198:
			v := raw32
			d.Uncorrectable = &v
		}
	}
}

// ataStatusFromSense decodes the ATA registers returned for SMART RETURN
// STATUS (check condition with an ATA Status Return sense descriptor, or
// fixed format sense). ok=false when the result is not recognized.
func ataStatusFromSense(sb []byte) (passed, ok bool) {
	if len(sb) < 8 {
		return false, false
	}
	var mid, high byte
	switch sb[0] & 0x7f {
	case 0x72, 0x73: // descriptor format
		n := int(sb[7])
		desc := sb[8:min(len(sb), 8+n)]
		for len(desc) >= 2 {
			l := int(desc[1]) + 2
			if l > len(desc) {
				return false, false
			}
			if desc[0] == 0x09 && l >= 14 {
				mid, high = desc[9], desc[11]
				break
			}
			desc = desc[l:]
		}
		if len(desc) < 2 {
			return false, false
		}
	case 0x70, 0x71: // fixed format
		if len(sb) < 12 {
			return false, false
		}
		mid, high = sb[10], sb[11]
	default:
		return false, false
	}
	switch {
	case mid == 0x4f && high == 0xc2:
		return true, true
	case mid == 0xf4 && high == 0x2c:
		return false, true
	}
	return false, false
}
