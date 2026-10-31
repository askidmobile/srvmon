// Package sshgw is the SSH side of srvmon-server: key generation, host key
// pinning, the one-time onboarding over a password login and interactive
// shells for the web terminal. Private keys never leave the server.
package sshgw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// DialTimeout bounds the TCP connect plus SSH handshake.
const DialTimeout = 15 * time.Second

// KeyPair is a freshly generated ed25519 key.
type KeyPair struct {
	Private     []byte // OpenSSH PEM
	Public      string // authorized_keys line
	Fingerprint string // SHA256:...
}

// GenerateKey creates an ed25519 key pair with the given comment.
func GenerateKey(comment string) (KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return KeyPair{}, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return KeyPair{}, err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return KeyPair{Private: pem.EncodeToMemory(block), Public: line, Fingerprint: ssh.FingerprintSHA256(sshPub)}, nil
}

// ParseHostKey parses a host key in authorized_keys format.
func ParseHostKey(s string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	return k, err
}

// FormatHostKey renders a host key in authorized_keys format.
func FormatHostKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// Addr joins host and port.
func Addr(host string, port int) string {
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(host, fmt.Sprint(port))
}

var errProbeDone = errors.New("probe done")

// ProbeHostKey connects to addr and returns the server's host key without
// authenticating, so the owner can compare the fingerprint before any
// password is sent (trust on first use, confirmed by a human).
func ProbeHostKey(ctx context.Context, addr string) (ssh.PublicKey, error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "srvmon-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errProbeDone
		},
		Timeout: DialTimeout,
	}
	c, err := dial(ctx, addr, cfg)
	if c != nil {
		c.Close()
	}
	if got != nil {
		return got, nil
	}
	if err == nil {
		err = errors.New("no host key")
	}
	return nil, err
}

// hostKeyAlgorithms makes the server present the same key type we pinned;
// otherwise it could pick another of its keys and fail the check.
func hostKeyAlgorithms(k ssh.PublicKey) []string {
	if k.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{k.Type()}
}

// Dial opens an SSH connection that only accepts the pinned host key.
func Dial(ctx context.Context, addr, user string, hostKey ssh.PublicKey, auth ...ssh.AuthMethod) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              auth,
		HostKeyCallback:   ssh.FixedHostKey(hostKey),
		HostKeyAlgorithms: hostKeyAlgorithms(hostKey),
		Timeout:           DialTimeout,
	}
	return dial(ctx, addr, cfg)
}

// DialKey connects with a stored private key (OpenSSH PEM).
func DialKey(ctx context.Context, addr, user string, hostKey ssh.PublicKey, privatePEM []byte) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey(privatePEM)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	return Dial(ctx, addr, user, hostKey, ssh.PublicKeys(signer))
}

func dial(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	// Abort the handshake if the caller goes away.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// Run executes cmd with optional stdin and returns stdout and stderr.
func Run(ctx context.Context, c *ssh.Client, cmd string, stdin io.Reader) (string, string, error) {
	sess, err := c.NewSession()
	if err != nil {
		return "", "", err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	sess.Stdin = stdin
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	err = sess.Run(cmd)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), errb.String(), err
}

// shq quotes s for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
