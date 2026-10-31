// Command srvmon-agent runs on every monitored server, collects host and
// docker metrics and pushes them to srvmon-server. It opens no ports and
// executes nothing on request.
//
// Environment:
//
//	SRVMON_URL       panel URL, e.g. https://mon.example.com (required)
//	SRVMON_TOKEN     agent token from `srvmon-server add-server` (required)
//	SRVMON_INTERVAL  push interval, default 5s
//	HOST_PROC        host procfs, default /proc (container: /host/proc)
//	HOST_ROOT        host root fs, default / (container: /host)
//	DOCKER_SOCK      docker socket, default /var/run/docker.sock; empty disables
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/askidmobile/srvmon/internal/agent"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	interval, err := time.ParseDuration(getenv("SRVMON_INTERVAL", "5s"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "SRVMON_INTERVAL:", err)
		os.Exit(2)
	}
	sock, ok := os.LookupEnv("DOCKER_SOCK")
	if !ok {
		sock = "/var/run/docker.sock"
	}
	cfg := agent.Config{
		URL:        os.Getenv("SRVMON_URL"),
		Token:      os.Getenv("SRVMON_TOKEN"),
		Interval:   interval,
		HostProc:   getenv("HOST_PROC", "/proc"),
		HostRoot:   getenv("HOST_ROOT", "/"),
		DockerSock: sock,
		Version:    version,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, cfg, log); err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-agent:", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
