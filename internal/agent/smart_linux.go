//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	nvmeIoctlAdminCmd = 0xC0484E41 // _IOWR('N', 0x41, struct nvme_passthru_cmd)
	sgIO              = 0x2285
	sgDxferNone       = -1
	sgDxferFromDev    = -3
)

// nvmePassthruCmd is struct nvme_passthru_cmd (linux/nvme_ioctl.h).
type nvmePassthruCmd struct {
	opcode      uint8
	flags       uint8
	rsvd1       uint16
	nsid        uint32
	cdw2        uint32
	cdw3        uint32
	metadata    uint64
	addr        uint64
	metadataLen uint32
	dataLen     uint32
	cdw10       uint32
	cdw11       uint32
	cdw12       uint32
	cdw13       uint32
	cdw14       uint32
	cdw15       uint32
	timeoutMs   uint32
	result      uint32
}

// sgIOHdr is struct sg_io_hdr (scsi/sg.h).
type sgIOHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         uintptr
	cmdp           uintptr
	sbp            uintptr
	timeout        uint32
	flags          uint32
	packID         int32
	usrPtr         uintptr
	status         uint8
	maskedStatus   uint8
	msgStatus      uint8
	sbLenWr        uint8
	hostStatus     uint16
	driverStatus   uint16
	resid          int32
	duration       uint32
	info           uint32
}

func openDisk(dev string) (*os.File, error) {
	f, err := os.OpenFile(dev, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, errSmartPermission
		}
		return nil, err
	}
	return f, nil
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if e != 0 {
		if e == syscall.EPERM || e == syscall.EACCES {
			return errSmartPermission
		}
		return e
	}
	if r != 0 {
		// NVMe passthrough returns the command's status code here when the
		// controller rejects it; the buffer then holds nothing valid.
		return fmt.Errorf("%w: device status %#x", errSmartUnsupported, r)
	}
	return nil
}

// nvmeSmartLog reads the SMART / Health Information log page.
func nvmeSmartLog(dev string) ([]byte, error) {
	f, err := openDisk(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, 512)
	cmd := nvmePassthruCmd{
		opcode:    0x02, // Get Log Page
		nsid:      0xffffffff,
		addr:      uint64(uintptr(unsafe.Pointer(&buf[0]))),
		dataLen:   uint32(len(buf)),
		cdw10:     uint32(len(buf)/4-1)<<16 | 0x02,
		timeoutMs: 5000,
	}
	err = ioctl(f.Fd(), nvmeIoctlAdminCmd, unsafe.Pointer(&cmd))
	runtime.KeepAlive(buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// ataPassThrough sends an ATA command in an ATA PASS-THROUGH (16) CDB.
// With data, 512 bytes are read (PIO data-in); without, the command is
// non-data and the ATA registers are returned in the sense buffer.
func ataPassThrough(f *os.File, feature, command, lbaMid, lbaHigh byte, data []byte) (sense []byte, err error) {
	cdb := [16]byte{0: 0x85, 4: feature, 10: lbaMid, 12: lbaHigh, 14: command}
	dir := int32(sgDxferNone)
	if data != nil {
		cdb[1] = 4 << 1 // PIO data-in
		cdb[2] = 0x0e   // t_dir from device, byte_block, length in sector count
		cdb[6] = 1
		dir = sgDxferFromDev
	} else {
		cdb[1] = 3 << 1 // non-data
		cdb[2] = 0x20   // ck_cond: return the registers
	}
	sb := make([]byte, 32)
	hdr := sgIOHdr{
		interfaceID:    'S',
		dxferDirection: dir,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        uint8(len(sb)),
		cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		sbp:            uintptr(unsafe.Pointer(&sb[0])),
		timeout:        10000,
	}
	if data != nil {
		hdr.dxferLen = uint32(len(data))
		hdr.dxferp = uintptr(unsafe.Pointer(&data[0]))
	}
	err = ioctl(f.Fd(), sgIO, unsafe.Pointer(&hdr))
	runtime.KeepAlive(&cdb)
	runtime.KeepAlive(sb)
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	if hdr.hostStatus != 0 {
		return nil, fmt.Errorf("sg host status %d", hdr.hostStatus)
	}
	sense = sb[:hdr.sbLenWr]
	if data != nil && hdr.status != 0 {
		// Check condition on a data command: the controller does not
		// pass ATA commands through (virtual disks, RAID).
		return sense, errSmartUnsupported
	}
	return sense, nil
}

// ataSmart checks the power mode (a sleeping disk is not woken up), reads
// the SMART attribute table and the overall health status.
func ataSmart(dev string) (data []byte, passed *bool, err error) {
	f, err := openDisk(dev)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	// CHECK POWER MODE: count 00h means standby.
	if sb, err := ataPassThrough(f, 0, 0xe5, 0, 0, nil); err == nil {
		if count, ok := ataCountFromSense(sb); ok && count == 0 {
			return nil, nil, errSmartStandby
		}
	} else if errors.Is(err, errSmartPermission) {
		return nil, nil, err
	}
	data = make([]byte, 512)
	if _, err := ataPassThrough(f, 0xd0, 0xb0, 0x4f, 0xc2, data); err != nil {
		return nil, nil, err
	}
	if sb, err := ataPassThrough(f, 0xda, 0xb0, 0x4f, 0xc2, nil); err == nil {
		if p, ok := ataStatusFromSense(sb); ok {
			passed = &p
		}
	}
	return data, passed, nil
}

// ataCountFromSense returns the ATA sector count register from the sense data.
func ataCountFromSense(sb []byte) (byte, bool) {
	if len(sb) < 8 {
		return 0, false
	}
	switch sb[0] & 0x7f {
	case 0x72, 0x73:
		desc := sb[8:min(len(sb), 8+int(sb[7]))]
		for len(desc) >= 2 {
			l := int(desc[1]) + 2
			if l > len(desc) {
				return 0, false
			}
			if desc[0] == 0x09 && l >= 14 {
				return desc[5], true
			}
			desc = desc[l:]
		}
	case 0x70, 0x71:
		if len(sb) >= 7 {
			return sb[6], true
		}
	}
	return 0, false
}
