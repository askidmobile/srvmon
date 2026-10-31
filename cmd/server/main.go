// Command srvmon-server is the central srvmon server: web UI, API, ingest.
//
//	srvmon-server serve                     run the HTTP server (default)
//	srvmon-server init                      set the panel password and Google Authenticator
//	srvmon-server add-server -name shop-db  register a server and print an agent token
//	srvmon-server healthcheck               exit 0 if /healthz answers (for Docker)
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	"github.com/askidmobile/srvmon/internal/api"
	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/config"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/selfmon"
	"github.com/askidmobile/srvmon/internal/store"
	"github.com/askidmobile/srvmon/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "init":
		err = initCreds()
	case "add-server":
		err = addServer(args)
	case "healthcheck":
		err = healthcheck()
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (serve | init | add-server | healthcheck | version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-server:", err)
		os.Exit(1)
	}
}

func openStore(cfg config.Config) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	return store.Open(cfg.DBPath())
}

func serve() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	var authLog io.Writer = os.Stderr
	if cfg.AuthLog != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.AuthLog), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(cfg.AuthLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("auth log: %w", err)
		}
		defer f.Close()
		authLog = io.MultiWriter(f, os.Stderr)
	}
	if cfg.SelfTokenFile != "" {
		created, err := selfmon.Ensure(context.Background(), st, cfg.SelfName, cfg.SelfTokenFile)
		if err != nil {
			return err
		}
		log.Info("self-monitoring", "server", cfg.SelfName, "new_token", created)
	}
	if _, err := st.GetSetting(context.Background(), store.KeyPasswordHash); errors.Is(err, store.ErrNotFound) {
		log.Warn("panel is not initialized: run `srvmon-server init` to set password and TOTP")
	}

	box, err := secret.LoadOrCreate(cfg.MasterKeyFile)
	if err != nil {
		return err
	}

	srv := api.New(api.Options{
		Config:  cfg,
		Store:   st,
		Log:     log,
		AuthLog: authLog,
		VM:      metrics.NewWriter(cfg.VMURL),
		VMRead:  metrics.NewReader(cfg.VMURL),
		Web:     web.Dist(),
		Version: version,
		Secrets: box,

		TelegramAPI: cfg.TelegramAPI,
	})
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = st.PurgeExpiredSessions(ctx)
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	srv.Start(ctx, cfg.Fail2banEvents, cfg.SelfName)
	log.Info("srvmon-server started", "listen", cfg.Listen, "version", version)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// initCreds interactively sets the password, TOTP secret and recovery codes.
func initCreds() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	in := bufio.NewReader(os.Stdin)

	if _, err := st.GetSetting(ctx, store.KeyPasswordHash); err == nil {
		fmt.Print("Panel is already initialized. Reset password and TOTP? All sessions will be dropped. [y/N]: ")
		if a, _ := in.ReadString('\n'); strings.ToLower(strings.TrimSpace(a)) != "y" {
			return errors.New("aborted")
		}
	}
	pw, err := readSecret(in, "New panel password (min 12 chars): ")
	if err != nil {
		return err
	}
	if len([]rune(pw)) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	pw2, err := readSecret(in, "Repeat password: ")
	if err != nil {
		return err
	}
	if pw != pw2 {
		return errors.New("passwords do not match")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}

	secret, err := auth.NewTOTPSecret()
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	uri := auth.TOTPURI(secret, "srvmon", host)
	fmt.Println("\nScan this QR code with Google Authenticator:")
	qrterminal.GenerateWithConfig(uri, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE, WhiteBlackChar: qrterminal.WHITE_BLACK, QuietZone: 1,
	})
	fmt.Printf("Or enter the key manually: %s\n\n", secret)

	for attempt := 0; ; attempt++ {
		fmt.Print("Enter the 6-digit code from the app to confirm: ")
		code, _ := in.ReadString('\n')
		if _, ok := auth.VerifyTOTP(secret, strings.TrimSpace(code), time.Now(), 0); ok {
			break
		}
		if attempt >= 2 {
			return errors.New("code does not match; check the phone clock and run init again")
		}
		fmt.Println("Code does not match, try again.")
	}

	codes, err := auth.NewRecoveryCodes(10)
	if err != nil {
		return err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashToken(c)
	}
	if err := st.SetCredentials(ctx, hash, secret, hashes); err != nil {
		return err
	}
	_ = st.Audit(ctx, "console", "init", "")
	fmt.Println("\nDone. One-time recovery codes (store them offline, they are shown once):")
	for _, c := range codes {
		fmt.Println("  " + c)
	}
	return nil
}

func readSecret(in *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b), err
	}
	s, err := in.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

// addServer registers a server card and prints a fresh agent token. For
// daily use the add wizard in the UI does this and installs the agent.
func addServer(args []string) error {
	fs := flag.NewFlagSet("add-server", flag.ContinueOnError)
	name := fs.String("name", "", "server name (required)")
	host := fs.String("host", "", "SSH host")
	group := fs.String("group", "", "group: prod, staging, infra, ...")
	project := fs.String("project", "", "project")
	desc := fs.String("description", "", "description")
	tags := fs.String("tags", "", "comma-separated tags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	var tagList []string
	for _, t := range strings.Split(*tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tagList = append(tagList, t)
		}
	}
	id, err := st.CreateServer(ctx, store.Server{Name: *name, Host: *host, Group: *group, Project: *project, Description: *desc, Tags: tagList})
	if err != nil {
		return err
	}
	token, err := auth.NewToken("a_")
	if err != nil {
		return err
	}
	if err := st.AddAgentToken(ctx, id, auth.HashToken(token)); err != nil {
		return err
	}
	fmt.Printf("Server %q added (id %d).\nAgent token (shown once): %s\n\n", *name, id, token)
	fmt.Println("On the server run the agent, e.g. with deploy/agent/docker-compose.yml:")
	fmt.Printf("  SRVMON_URL=https://<panel-domain> SRVMON_TOKEN=%s docker compose up -d\n", token)
	return nil
}

func healthcheck() error {
	addr := os.Getenv("SRVMON_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}
