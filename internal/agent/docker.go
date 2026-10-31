package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// Docker lists containers through the Docker Engine API on a unix socket.
// Only read endpoints are used; the socket should be mounted read-only.
type Docker struct {
	client *http.Client
}

// NewDocker returns a client for the socket path.
func NewDocker(sock string) *Docker {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Docker{client: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
}

type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
	Ports  []dockerPort      `json:"Ports"`
}

type dockerPort struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// formatPorts renders published ports as "8080→80/tcp" and exposed ones as
// "6379/tcp", without the IPv4/IPv6 duplicates.
func formatPorts(ps []dockerPort) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ps {
		s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		if p.PublicPort != 0 {
			s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func (d *Docker) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Containers returns all containers (running and stopped).
func (d *Docker) Containers(ctx context.Context) ([]protocol.Container, error) {
	var list []dockerContainer
	if err := d.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	out := make([]protocol.Container, 0, len(list))
	for _, c := range list {
		name := c.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, protocol.Container{
			ID:      c.ID,
			Name:    name,
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Health:  healthFromStatus(c.Status),
			Ports:   formatPorts(c.Ports),
			Compose: c.Labels["com.docker.compose.project"],
		})
	}
	return out, nil
}

func healthFromStatus(s string) string {
	switch {
	case strings.Contains(s, "(healthy)"):
		return "healthy"
	case strings.Contains(s, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(s, "(health: starting)"):
		return "starting"
	}
	return ""
}
