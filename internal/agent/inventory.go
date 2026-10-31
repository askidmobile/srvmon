package agent

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// Inventory reads slowly changing host facts for the server passport.
func Inventory(ctx context.Context, hostProc, hostRoot string, dock *Docker) *protocol.Inventory {
	inv := &protocol.Inventory{}
	for _, p := range []string{"etc/os-release", "usr/lib/os-release"} {
		if b, err := os.ReadFile(filepath.Join(hostRoot, p)); err == nil {
			inv.OS = osPrettyName(b)
			break
		}
	}
	if b, err := os.ReadFile(filepath.Join(hostProc, "sys/kernel/osrelease")); err == nil {
		inv.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(hostProc, "cpuinfo")); err == nil {
		inv.CPUModel = cpuModel(b)
	}
	if dock != nil {
		inv.DockerVersion, _ = dock.Version(ctx)
	}
	inv.IPs = hostIPs()
	return inv
}

func osPrettyName(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func cpuModel(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Hardware", "Model":
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

// hostIPs lists addresses of physical-looking interfaces. The agent runs with
// network_mode: host, so these are the host's.
func hostIPs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || skipIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

func skipIface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "tun", "wg"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
