package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/sshgw"
)

// One-time login for the add wizard. Servers often have password logins
// switched off, so besides the password the owner can log in with their
// own private key (held in memory for this one job, never stored) or add
// the panel's public key to a sudoer's authorized_keys by hand.

const (
	maxPrivateKey = 16 << 10
	setupKeyTTL   = time.Hour
	maxSetupKeys  = 32
)

type loginAuth struct {
	method   string
	signer   ssh.Signer     // "key" and "panel_key"
	panelKey *sshgw.KeyPair // "panel_key": also becomes the srvmon key
}

func (l loginAuth) forgotten() string {
	switch l.method {
	case "key":
		return "ваш ключ и пароль sudo удалены из памяти, в бд и логи не записаны"
	case "panel_key":
		return "пароль sudo (если был) удалён из памяти, в бд и логи не записан"
	}
	return "пароль удалён из памяти, в бд и логи не записан"
}

// loginFor validates the login part of an onboarding request and returns
// an error code for the client.
func (s *Server) loginFor(req *onboardReq) (loginAuth, string) {
	l := loginAuth{method: req.Method}
	switch req.Method {
	case "", "password":
		l.method = "password"
		if req.Password == "" {
			return l, "password_required"
		}
	case "key":
		pk := strings.TrimSpace(req.PrivateKey)
		req.PrivateKey = ""
		if pk == "" {
			return l, "private_key_required"
		}
		if len(pk) > maxPrivateKey {
			return l, "bad_private_key"
		}
		if strings.HasPrefix(pk, "ssh-") || strings.HasPrefix(pk, "ecdsa-") {
			return l, "public_key_given" // the .pub file, not the private key
		}
		var signer ssh.Signer
		var err error
		if req.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(pk), []byte(req.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(pk))
		}
		req.Passphrase = ""
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing):
			return l, "passphrase_required"
		case err != nil && strings.Contains(err.Error(), "password"):
			return l, "bad_passphrase"
		case err != nil:
			return l, "bad_private_key"
		}
		l.signer = signer
	case "panel_key":
		kp, ok := s.setupKeys.get(req.SetupKey)
		if !ok {
			return l, "setup_key_expired"
		}
		signer, err := ssh.ParsePrivateKey(kp.Private)
		if err != nil {
			return l, "setup_key_expired"
		}
		l.signer, l.panelKey = signer, &kp
	default:
		return l, "bad_method"
	}
	return l, ""
}

// setupKeys holds panel keys shown to the owner but not yet installed.
// They live only in memory: a restart just means showing a new key.
type setupKeys struct {
	mu sync.Mutex
	m  map[string]setupKey
}

type setupKey struct {
	kp      sshgw.KeyPair
	created time.Time
}

func (k *setupKeys) gc(now time.Time) {
	for id, v := range k.m {
		if now.Sub(v.created) > setupKeyTTL {
			clear(v.kp.Private)
			delete(k.m, id)
		}
	}
}

func (k *setupKeys) add(kp sshgw.KeyPair) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gc(time.Now())
	if len(k.m) >= maxSetupKeys {
		return "", errTooManyJobs
	}
	id, err := auth.NewToken("k_")
	if err != nil {
		return "", err
	}
	k.m[id] = setupKey{kp: kp, created: time.Now()}
	return id, nil
}

func (k *setupKeys) get(id string) (sshgw.KeyPair, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gc(time.Now())
	v, ok := k.m[id]
	if !ok {
		return sshgw.KeyPair{}, false
	}
	// A copy: the job keeps its key even if this entry expires meanwhile.
	kp := v.kp
	kp.Private = append([]byte(nil), v.kp.Private...)
	return kp, true
}

func (k *setupKeys) drop(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[id]; ok {
		clear(v.kp.Private)
		delete(k.m, id)
	}
}

// handleSetupKey creates a panel key for the "add the key yourself" login.
// The owner puts the public part into a sudoer's authorized_keys; after
// onboarding the same key belongs to the srvmon account only.
func (s *Server) handleSetupKey(w http.ResponseWriter, r *http.Request) {
	kp, err := sshgw.GenerateKey("srvmon@" + s.cfg.SelfName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	id, err := s.setupKeys.add(kp)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "busy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id":          id,
		"public_key":  kp.Public,
		"fingerprint": kp.Fingerprint,
		"command": "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '" + kp.Public +
			"' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys",
	})
}
