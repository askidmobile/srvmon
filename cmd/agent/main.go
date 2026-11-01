// Command srvmon-agent runs on every monitored server, collects host and
// docker metrics and pushes them to srvmon-server. It opens no ports and
// executes nothing on request.
//
// Environment:
//
//	SRVMON_URL       panel URL, e.g. https://mon.example.com (required)
//	SRVMON_TOKEN     agent token from `srvmon-server add-server`
//	SRVMON_TOKEN_FILE  file with the token instead of SRVMON_TOKEN; the agent
//	                 waits until it appears (self-monitoring in the server stack)
//	SRVMON_INTERVAL  push interval, default 5s
//	HOST_PROC        host procfs, default /proc (container: /host/proc)
//	HOST_ROOT        host root fs, default / (container: /host)
//	DOCKER_SOCK      docker socket, default /var/run/docker.sock; empty disables
//	SRVMON_SMART     1 reads S.M.A.R.T. of the host's disks every 30 min; needs
//	                 --cap-add SYS_RAWIO --cap-add SYS_ADMIN and read access to
//	                 block devices (--device-cgroup-rule 'b 8:* r' 'b 259:* r')
//
// Subcommands (same image, other containers):
//
//	dockerproxy      the srvmon-dockerproxy container: serves the agent a
//	                 filtered read-only Docker API on DOCKERPROXY_LISTEN
//	                 (default /run/srvmon/docker.sock) from DOCKERPROXY_UPSTREAM
//	                 (default /var/run/docker.sock)
//	write-sudoers U  a one-off container of the panel: writes stdin as the
//	                 sudoers file of user U into SUDOERS_DIR (the host's
//	                 /etc/sudoers.d), or removes it when stdin is empty
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/askidmobile/srvmon/internal/agent"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "dockerproxy":
			os.Exit(runDockerProxy(log))
		case "write-sudoers":
			os.Exit(runWriteSudoers(os.Args[2:], os.Stdin, getenv("SUDOERS_DIR", "/sudoers.d")))
		}
	}
	interval, err := time.ParseDuration(getenv("SRVMON_INTERVAL", "5s"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "SRVMON_INTERVAL:", err)
		os.Exit(2)
	}
	sock, ok := os.LookupEnv("DOCKER_SOCK")
	if !ok {
		sock = "/var/run/docker.sock"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := os.Getenv("SRVMON_TOKEN")
	tokenFile := ""
	if f := os.Getenv("SRVMON_TOKEN_FILE"); token == "" && f != "" {
		tokenFile = f
		if token, err = waitToken(ctx, f, log); err != nil {
			fmt.Fprintln(os.Stderr, "srvmon-agent:", err)
			os.Exit(1)
		}
	}
	cfg := agent.Config{
		URL:        os.Getenv("SRVMON_URL"),
		Token:      token,
		TokenFile:  tokenFile,
		Interval:   interval,
		HostProc:   getenv("HOST_PROC", "/proc"),
		HostRoot:   getenv("HOST_ROOT", "/"),
		DockerSock: sock,
		Smart:      os.Getenv("SRVMON_SMART") == "1" || os.Getenv("SRVMON_SMART") == "true",
		Version:    version,
	}
	if err := agent.Run(ctx, cfg, log); err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-agent:", err)
		os.Exit(1)
	}
}

// waitToken reads the token file, waiting while it does not exist yet.
func waitToken(ctx context.Context, path string, log *slog.Logger) (string, error) {
	for warned := false; ; {
		b, err := os.ReadFile(path)
		if err == nil {
			if tok := strings.TrimSpace(string(b)); tok != "" {
				return tok, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("SRVMON_TOKEN_FILE: %w", err)
		}
		if !warned {
			log.Info("waiting for agent token file", "path", path)
			warned = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func runDockerProxy(log *slog.Logger) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listen := getenv("DOCKERPROXY_LISTEN", "/run/srvmon/docker.sock")
	upstream := getenv("DOCKERPROXY_UPSTREAM", "/var/run/docker.sock")
	if err := agent.ServeDockerProxy(ctx, listen, upstream, log); err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-agent dockerproxy:", err)
		return 1
	}
	return 0
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
