package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// The AI agent's read-only commands (and MCP's) run as sshgw.ReaderUser,
// not as the panel's SSH user: that one is usually in the docker group,
// which is root-equivalent, while the reader has no groups and no sudo.
// The classifier and Redact decide what may run and what the model sees;
// the reader's OS permissions bound what a command that runs can read.

const (
	readerSelfTTL   = time.Hour        // the SSH user itself has no root: recheck hourly
	readerFailedTTL = 10 * time.Minute // a failed setup is retried after this
)

type readerState struct {
	self   bool  // run as the SSH user: it has no root access to set the reader up
	failed error // the setup failed
	until  time.Time
}

func (s *Server) readerStateOf(id int64) readerState {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	st := s.agent.readers[id]
	if time.Now().After(st.until) {
		return readerState{}
	}
	return st
}

func (s *Server) setReaderState(id int64, st readerState) {
	s.agent.mu.Lock()
	s.agent.readers[id] = st
	s.agent.mu.Unlock()
}

func isAuthError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unable to authenticate")
}

// readerClient connects as the reader, setting it up on first use. asReader
// is false only when the SSH user has no root access at all, so it is as
// unprivileged as the reader would be.
func (s *Server) readerClient(ctx context.Context, a store.SSHAccess) (c *ssh.Client, asReader bool, err error) {
	st := s.readerStateOf(a.ServerID)
	if st.failed != nil {
		return nil, false, st.failed
	}
	if st.self {
		c, err := s.dialServer(ctx, a)
		return c, false, err
	}
	ra := a
	ra.User = sshgw.ReaderUser
	c, err = s.dialServer(ctx, ra)
	if err == nil || !isAuthError(err) {
		return c, err == nil, err
	}
	pc, err := s.dialServer(ctx, a)
	if err != nil {
		return nil, false, err
	}
	pub, err := s.serverPublicKey(a)
	if err != nil {
		pc.Close()
		return nil, false, err
	}
	err = sshgw.EnsureReader(ctx, pc, sshgw.ReaderParams{PublicKey: pub, Image: s.cfg.AgentImage,
		RegistryUser: s.cfg.AgentRegistryUser, RegistryToken: s.cfg.AgentRegistryToken})
	if errors.Is(err, sshgw.ErrNoRootAccess) {
		s.setReaderState(a.ServerID, readerState{self: true, until: time.Now().Add(readerSelfTTL)})
		_ = s.store.Audit(context.Background(), "agent", "reader_self", a.Name+": "+a.User+" has no root, docker or sudo: it runs read-only commands itself")
		return pc, false, nil
	}
	pc.Close()
	if err == nil {
		c, err = s.dialServer(ctx, ra)
		if err != nil {
			err = i18n.Errorf("%s is set up but cannot sign in (sshd AllowUsers/AllowGroups?): %s", sshgw.ReaderUser, err.Error())
		}
	}
	if err != nil && ctx.Err() != nil {
		// The caller gave up (request cancelled, timeout): that says nothing
		// about the server, so it is not cached as a setup failure.
		return nil, false, err
	}
	if err != nil {
		err = i18n.Errorf("the read-only account is not available, read-only commands do not run: %s", err.Error())
		s.setReaderState(a.ServerID, readerState{failed: err, until: time.Now().Add(readerFailedTTL)})
		s.log.Warn("reader account", "server", a.Name, "err", err)
		return nil, false, err
	}
	_ = s.store.Audit(context.Background(), "agent", "reader_setup", a.Name+": "+sshgw.ReaderUser)
	return c, true, nil
}

// serverPublicKey is the authorized_keys line of the panel's key for a.
func (s *Server) serverPublicKey(a store.SSHAccess) (string, error) {
	if s.secrets == nil {
		return "", errors.New(i18n.T("the master key is not loaded"))
	}
	priv, err := s.secrets.Open(a.KeyEnc, keyAAD(a.ServerID))
	if err != nil {
		return "", err
	}
	defer clear(priv)
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " srvmon-ro", nil
}

// secretTarget resolves the words of a read-only command on the server
// (symlinks included) and returns why it needs confirmation when one of
// them points to a file that usually holds secrets. Classify only sees the
// words as written.
func secretTarget(ctx context.Context, c *ssh.Client, cmd string) string {
	words := aiagent.PathWords(cmd)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	for _, w := range words {
		fmt.Fprintf(&b, "readlink -f -- %s 2>/dev/null || echo\n", sshgw.Quote(w))
	}
	out, _, err := sshgw.Run(ctx, c, "sh -s", strings.NewReader(b.String()))
	if err != nil && out == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, w := range words {
		if i < len(lines) && lines[i] != "" && aiagent.SecretPath(lines[i]) {
			return i18n.T("%s points to %s, which may hold secrets", w, lines[i])
		}
	}
	return ""
}

// runReadOnly runs a command Classify accepted. confirm is non-empty when a
// resolved path turned out to be a secret file: the caller treats the
// command as changing (it waits for the owner) instead.
func (s *Server) runReadOnly(ctx context.Context, a store.SSHAccess, cmd string, limit time.Duration) (code int, out, confirm string, err error) {
	ctx, cancel := context.WithTimeout(ctx, limit+30*time.Second)
	defer cancel()
	c, asReader, err := s.readerClient(ctx, a)
	if err != nil {
		return -1, "", "", err
	}
	defer c.Close()
	// Close the connection when ctx ends, also while no command runs on it:
	// a forward request (ListenUnix) waits for sshd without a context.
	defer context.AfterFunc(ctx, func() { c.Close() })()
	if why := secretTarget(ctx, c, cmd); why != "" {
		return -1, "", why, nil
	}
	env := ""
	if asReader {
		stop, sock := s.dockerBridge(c, a)
		if stop != nil {
			defer func() {
				stop()
				_, _, _ = sshgw.Run(context.Background(), c, "rm -f "+sshgw.Quote(sock), nil)
			}()
			env = "DOCKER_HOST=unix://" + sock + " "
		}
	}
	code, out, err = s.capture(ctx, c, env, cmd, limit)
	return code, out, "", err
}

// dockerBridge serves the reader a filtered Docker API on a fresh socket;
// the daemon is reached over the SSH user's connection, opened on the first
// docker request. Returns nil when sshd refuses the forward.
func (s *Server) dockerBridge(reader *ssh.Client, a store.SSHAccess) (stop func(), sock string) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	sock = "/tmp/srvmon-docker-" + hex.EncodeToString(b) + ".sock"
	var (
		mu     sync.Mutex
		up     *ssh.Client
		closed bool
	)
	dial := func(ctx context.Context) (net.Conn, error) {
		mu.Lock()
		if closed {
			mu.Unlock()
			return nil, errors.New("docker bridge closed")
		}
		if up == nil {
			// Bounded by the SSH dial timeout.
			c, err := s.dialServer(ctx, a)
			if err != nil {
				mu.Unlock()
				return nil, err
			}
			up = c
		}
		c := up
		mu.Unlock()
		// The channel open has no context: it runs without the lock, so
		// stop can close the connection and end it.
		return c.Dial("unix", "/var/run/docker.sock")
	}
	stopBridge, err := sshgw.DockerBridge(reader, sock, sshgw.NewDockerReadHandler(dial, s.log))
	if err != nil {
		return nil, ""
	}
	return func() {
		mu.Lock()
		closed = true
		c := up
		mu.Unlock()
		if c != nil {
			c.Close()
		}
		stopBridge()
	}, sock
}
