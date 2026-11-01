package agent

import (
	"encoding/binary"
	"testing"

	"github.com/askidmobile/srvmon/internal/protocol"
)

func TestParseNVMeLog(t *testing.T) {
	b := make([]byte, 512)
	b[0] = 0
	binary.LittleEndian.PutUint16(b[1:], 273+41)
	b[3], b[4], b[5] = 100, 10, 7
	binary.LittleEndian.PutUint64(b[128:], 12345)
	binary.LittleEndian.PutUint64(b[160:], 2)
	var d protocol.DiskHealth
	parseNVMeLog(b, &d)
	if d.Passed == nil || !*d.Passed || *d.TempC != 41 || *d.PercentUsed != 7 || *d.AvailableSpare != 100 ||
		*d.SpareThreshold != 10 || *d.PowerOnHours != 12345 || *d.MediaErrors != 2 {
		t.Fatalf("%+v", d)
	}
	b[0] = 0x04
	d = protocol.DiskHealth{}
	parseNVMeLog(b, &d)
	if *d.Passed || d.CriticalWarning != 4 {
		t.Fatalf("critical warning: %+v", d)
	}
}

func TestParseATAData(t *testing.T) {
	b := make([]byte, 512)
	attr := func(i int, id byte, raw ...byte) {
		a := b[2+i*12:]
		a[0] = id
		copy(a[5:11], raw)
	}
	attr(0, 5, 8)
	attr(1, 9, 0x10, 0x27) // 10000 h
	attr(2, 194, 36, 0, 20, 0, 45, 0)
	attr(3, 197, 1)
	attr(4, 198, 0)
	var d protocol.DiskHealth
	parseATAData(b, &d)
	if *d.Reallocated != 8 || *d.PowerOnHours != 10000 || *d.TempC != 36 || *d.Pending != 1 || *d.Uncorrectable != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestATAStatusFromSense(t *testing.T) {
	desc := func(mid, high byte) []byte {
		sb := make([]byte, 22)
		sb[0], sb[7] = 0x72, 14
		d := sb[8:]
		d[0], d[1] = 0x09, 0x0c
		d[9], d[11] = mid, high
		return sb
	}
	if p, ok := ataStatusFromSense(desc(0x4f, 0xc2)); !ok || !p {
		t.Fatal("passed not recognized")
	}
	if p, ok := ataStatusFromSense(desc(0xf4, 0x2c)); !ok || p {
		t.Fatal("failed not recognized")
	}
	if _, ok := ataStatusFromSense(desc(0, 0)); ok {
		t.Fatal("garbage recognized")
	}
	fixed := make([]byte, 18)
	fixed[0], fixed[10], fixed[11] = 0x70, 0x4f, 0xc2
	if p, ok := ataStatusFromSense(fixed); !ok || !p {
		t.Fatal("fixed format not recognized")
	}
	// CHECK POWER MODE count register.
	sb := desc(0, 0)
	sb[8+5] = 0xff
	if c, ok := ataCountFromSense(sb); !ok || c != 0xff {
		t.Fatalf("count = %x %v", c, ok)
	}
}

func TestReadSmartDiscovery(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"sda", "sdb", "nvme0n1", "nvme0c0n1", "loop0", "dm-0", "sda1"} {
		writeFile(t, root+"/sys/block/"+n+"/removable", "0\n")
	}
	writeFile(t, root+"/sys/block/sdb/removable", "1\n")
	writeFile(t, root+"/sys/block/sda/device/vendor", "ATA     \n")
	writeFile(t, root+"/sys/block/sda/device/model", "Samsung SSD 870\n")
	writeFile(t, root+"/sys/block/nvme0n1/device/model", "WD Black SN850\n")
	got := ReadSmart(root)
	if len(got) != 2 || got[0].Device != "nvme0n1" || got[1].Device != "sda" {
		t.Fatalf("disks = %+v", got)
	}
	if got[1].Model != "Samsung SSD 870" || got[0].Model != "WD Black SN850" {
		t.Fatalf("models = %q %q", got[0].Model, got[1].Model)
	}
	// No device nodes in the temp root: every disk reports an error.
	for _, d := range got {
		if d.Error == "" {
			t.Fatalf("%s: no error", d.Device)
		}
	}
}
