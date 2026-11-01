package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
	"golang.org/x/crypto/ssh"
)

type sshPublicKey = ssh.PublicKey

func sshFingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// sshUser is the account srvmon creates on every server.
const sshUser = "srvmon"

const (
	onboardTimeout  = 10 * time.Minute
	heartbeatWait   = 90 * time.Second
	jobRetention    = 30 * time.Minute
	maxFieldLen     = 200
	onboardRunLimit = 4
)

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	hostRe = regexp.MustCompile(`^[A-Za-z0-9.:\[\]_-]{1,253}$`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// cardInput is the server passport from the wizard.
type cardInput struct {
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Project     string   `json:"project"`
	Group       string   `json:"group"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Provider    string   `json:"provider"`
	Plan        string   `json:"plan"`
	Cost        *float64 `json:"cost"`
	Currency    string   `json:"currency"`
	Period      string   `json:"period"`
	NextPayment string   `json:"next_payment"`
	PanelURL    string   `json:"panel_url"`
}

// normalize trims fields and validates them; returns an error code.
func (c *cardInput) normalize() string {
	trim := func(p *string) { *p = strings.TrimSpace(*p) }
	for _, p := range []*string{&c.Name, &c.Host, &c.Project, &c.Group, &c.Description, &c.Provider,
		&c.Plan, &c.Currency, &c.Period, &c.NextPayment, &c.PanelURL} {
		trim(p)
	}
	c.Host = strings.TrimSuffix(strings.TrimPrefix(c.Host, "["), "]")
	if c.Port == 0 {
		c.Port = 22
	}
	switch {
	case !nameRe.MatchString(c.Name):
		return "bad_name"
	case !hostRe.MatchString(c.Host):
		return "bad_host"
	case c.Port < 1 || c.Port > 65535:
		return "bad_port"
	case c.NextPayment != "" && !validDate(c.NextPayment):
		return "bad_date"
	}
	// The passport part follows the same rules as the passport editor, so a
	// card created here stays editable on the server screen.
	pc := store.ServerCard{
		Description: c.Description, Project: c.Project, Group: c.Group, Tags: c.Tags, Provider: c.Provider,
		Plan: c.Plan, Cost: c.Cost, Currency: strings.ToUpper(c.Currency), NextPayment: c.NextPayment,
		Period: c.Period, PanelURL: c.PanelURL,
	}
	if field := validateCard(&pc); field != "" {
		if code, ok := cardFieldCodes[field]; ok {
			return code
		}
		return "field_too_long"
	}
	c.Description, c.Project, c.Group, c.Tags, c.Provider = pc.Description, pc.Project, pc.Group, pc.Tags, pc.Provider
	c.Plan, c.Cost, c.Currency, c.NextPayment, c.Period, c.PanelURL = pc.Plan, pc.Cost, pc.Currency, pc.NextPayment, pc.Period, pc.PanelURL
	return ""
}

var cardFieldCodes = map[string]string{
	"next_payment": "bad_date",
	"cost":         "bad_cost",
	"description":  "bad_description",
	"tags":         "bad_tag",
	"period":       "bad_period",
	"panel_url":    "bad_url",
}

func validDate(s string) bool {
	if !dateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func (c cardInput) server() store.Server {
	return store.Server{
		Name: c.Name, Description: c.Description, Project: c.Project, Group: c.Group, Tags: c.Tags,
		Host: c.Host, Port: c.Port, Provider: c.Provider, Plan: c.Plan, Cost: c.Cost, Currency: c.Currency,
		NextPayment: c.NextPayment, Period: c.Period, PanelURL: c.PanelURL,
	}
}

func validPeriod(p string) bool {
	switch p {
	case "", "month", "year", "hour":
		return true
	}
	return false
}

// validPanelURL allows an empty value or an absolute http(s) URL (the link
// is rendered as href, so javascript: and friends must not get through).
func validPanelURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && len(s) <= maxFieldLen
}

// --- probe: show the host key before any password is sent ---

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	c := cardInput{Name: "probe", Host: req.Host, Port: req.Port}
	if code := c.normalize(); code != "" {
		writeError(w, http.StatusBadRequest, code)
		return
	}
	key, err := sshgw.ProbeHostKey(r.Context(), sshgw.Addr(c.Host, c.Port))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "unreachable", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"host_key":    sshgw.FormatHostKey(key),
		"fingerprint": sshFingerprint(key),
		"type":        key.Type(),
	})
}

// --- onboarding jobs ---

type jobLine struct {
	State string `json:"state"`
	Text  string `json:"text"`
	TS    int64  `json:"ts"`
}

type job struct {
	mu    sync.Mutex
	lines []jobLine
	done  bool
	// committed: the server is set up and its key is being saved; from here
	// on the job cannot be cancelled (a rollback would undo a working setup).
	committed bool
	ok        bool
	serverID  int64
	cancel    context.CancelFunc
	finished  time.Time
}

func (j *job) log(state, text string) {
	j.mu.Lock()
	j.lines = append(j.lines, jobLine{State: state, Text: text, TS: time.Now().Unix()})
	j.mu.Unlock()
}

func (j *job) finish(ok bool) {
	j.mu.Lock()
	j.done, j.ok, j.finished = true, ok, time.Now()
	j.mu.Unlock()
}

type jobs struct {
	mu sync.Mutex
	m  map[string]*job
}

func (js *jobs) add(j *job) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	js.mu.Lock()
	defer js.mu.Unlock()
	running := 0
	sameServer := false
	for k, old := range js.m {
		old.mu.Lock()
		expired := old.done && time.Since(old.finished) > jobRetention
		if !old.done {
			running++
			sameServer = sameServer || old.serverID == j.serverID
		}
		old.mu.Unlock()
		if expired {
			delete(js.m, k)
		}
	}
	// Checked under the same lock as the insert: two requests for one
	// server must not both start (they would revoke each other's tokens).
	if sameServer {
		return "", errServerBusy
	}
	if running >= onboardRunLimit {
		return "", errTooManyJobs
	}
	js.m[id] = j
	return id, nil
}

var (
	errTooManyJobs = errors.New("too many onboarding jobs")
	errServerBusy  = errors.New("server is being onboarded")
)

// active reports whether an unfinished onboarding job targets the server.
func (js *jobs) active(serverID int64) bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	for _, j := range js.m {
		j.mu.Lock()
		busy := !j.done && j.serverID == serverID
		j.mu.Unlock()
		if busy {
			return true
		}
	}
	return false
}

func (js *jobs) get(id string) *job {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.m[id]
}

type onboardReq struct {
	ServerID int64     `json:"server_id"` // attach SSH to an existing card
	Card     cardInput `json:"card"`
	User     string    `json:"user"`
	// Method is how the one-time login works: "password" (default), "key"
	// (the owner's private key, used once and never stored) or "panel_key"
	// (the owner added the panel key from /setup-key to User's
	// authorized_keys). With a key, Password is only the sudo password.
	Method          string `json:"method"`
	Password        string `json:"password"`
	PrivateKey      string `json:"private_key"`
	Passphrase      string `json:"passphrase"`
	SetupKey        string `json:"setup_key"`
	HostKey         string `json:"host_key"` // from probe, confirmed by the owner
	Sudo            bool   `json:"sudo"`
	DisablePassword bool   `json:"disable_password"`
	InstallAgent    bool   `json:"install_agent"`
}

func (s *Server) handleOnboard(w http.ResponseWriter, r *http.Request) {
	var req onboardReq
	if err := readJSONLimit(r, &req, onboardBodyLimit); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if code := req.Card.normalize(); code != "" {
		writeError(w, http.StatusBadRequest, code)
		return
	}
	req.User = strings.TrimSpace(req.User)
	if req.User == "" || len(req.User) > 64 || strings.ContainsAny(req.User, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "bad_user")
		return
	}
	login, code := s.loginFor(&req)
	if code != "" {
		writeError(w, http.StatusBadRequest, code)
		return
	}
	hostKey, err := sshgw.ParseHostKey(req.HostKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_host_key")
		return
	}
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
		return
	}

	ctx := r.Context()
	created := false
	id := req.ServerID
	if id == 0 {
		id, err = s.store.CreateServer(ctx, req.Card.server())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeError(w, http.StatusConflict, "name_taken")
				return
			}
			s.log.Error("create server", "err", err)
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		created = true
	} else if ok, err := s.store.ServerExists(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}

	jctx, cancel := context.WithTimeout(context.Background(), onboardTimeout)
	j := &job{serverID: id, cancel: cancel}
	jobID, err := s.addOnboardJob(j)
	if err != nil {
		cancel()
		if created {
			_ = s.store.DeleteServer(ctx, id)
		}
		if errors.Is(err, errTooManyJobs) {
			writeError(w, http.StatusTooManyRequests, "busy")
			return
		}
		if errors.Is(err, errServerBusy) {
			writeError(w, http.StatusConflict, "onboarding")
			return
		}
		if errors.Is(err, errAgentUpdating) {
			writeError(w, http.StatusConflict, "update_running")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ip := s.clientIP(r)
	_ = s.store.Audit(ctx, ip, "server_onboard_start", req.Card.Name)
	agentURL := s.publicURL(r)
	go s.runOnboard(jctx, j, req, login, hostKey, created, ip, agentURL)
	writeJSON(w, http.StatusAccepted, map[string]any{"job": jobID, "server_id": id})
}

// publicURL is where agents reach the panel.
func (s *Server) publicURL(r *http.Request) string {
	if s.cfg.PublicOrigin != "" {
		return strings.TrimRight(s.cfg.PublicOrigin, "/")
	}
	scheme := "http"
	if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) runOnboard(ctx context.Context, j *job, req onboardReq, login loginAuth, hostKey sshPublicKey, created bool, ip, agentURL string) {
	defer j.cancel()
	id := j.serverID
	bg := context.Background()
	fail := func(text string) {
		j.log(sshgw.StepFail, text)
		if created {
			if err := s.store.DeleteServer(bg, id); err == nil {
				j.log(sshgw.StepInfo, i18n.T("server card deleted (rollback)"))
			}
		}
		_ = s.store.Audit(bg, ip, "server_onboard_failed", req.Card.Name)
		j.finish(false)
	}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("onboard panic", "panic", p)
			fail(i18n.T("internal error"))
		}
	}()

	var key sshgw.KeyPair
	if login.panelKey != nil {
		key = *login.panelKey
		j.log(sshgw.StepOK, i18n.T("panel key: %s", key.Fingerprint))
	} else {
		var err error
		key, err = sshgw.GenerateKey("srvmon@" + s.cfg.SelfName)
		if err != nil {
			fail(i18n.T("key generation: %s", err.Error()))
			return
		}
		j.log(sshgw.StepOK, i18n.T("ed25519 key generated: %s", key.Fingerprint))
	}

	params := sshgw.OnboardParams{
		Addr:            sshgw.Addr(req.Card.Host, req.Card.Port),
		User:            req.User,
		Password:        req.Password,
		LoginKey:        login.signer,
		DropLoginKey:    login.panelKey != nil,
		HostKey:         hostKey,
		Key:             key,
		SSHUser:         sshUser,
		Sudo:            req.Sudo,
		DisablePassword: req.DisablePassword,
	}
	// The commit point: the key login works, nothing optional has run yet.
	// After this, cancelling no longer rolls back (sshd may be about to stop
	// accepting the password, so the key has to be kept).
	params.Commit = func(ctx context.Context) error {
		j.mu.Lock()
		if err := ctx.Err(); err != nil {
			j.mu.Unlock()
			return err
		}
		j.committed = true
		j.mu.Unlock()
		sealed, err := s.secrets.Seal(key.Private, keyAAD(id))
		if err != nil {
			return i18n.Errorf("key encryption: %w", err)
		}
		if err := s.store.SetServerSSH(bg, id, req.Card.Host, req.Card.Port, sshUser, sshgw.FormatHostKey(hostKey), sealed); err != nil {
			return i18n.Errorf("saving the key: %w", err)
		}
		j.log(sshgw.StepOK, i18n.T("private key saved encrypted (XChaCha20-Poly1305); the setup is committed, cancel no longer rolls back"))
		if req.SetupKey != "" {
			s.setupKeys.drop(req.SetupKey)
		}
		return nil
	}
	req.Password = ""

	var tokenHash string
	if req.InstallAgent {
		token, err := auth.NewToken("a_")
		if err != nil {
			fail(i18n.T("agent token: %s", err.Error()))
			return
		}
		tokenHash = auth.HashToken(token)
		if err := s.store.AddAgentToken(bg, id, tokenHash); err != nil {
			fail(i18n.T("agent token: %s", err.Error()))
			return
		}
		params.Agent = &sshgw.AgentInstall{
			Image: s.cfg.AgentImage, URL: agentURL, Token: token,
			RegistryUser: s.cfg.AgentRegistryUser, RegistryToken: s.cfg.AgentRegistryToken,
		}
	}

	res, err := sshgw.Onboard(ctx, params, j.log)
	params.Password = ""
	params.LoginKey = nil
	if err != nil {
		if tokenHash != "" && !created {
			_ = s.store.RevokeAgentToken(bg, tokenHash)
		}
		if ctx.Err() != nil {
			fail(i18n.T("cancelled or timed out; user %s may be left on the server", sshUser))
		} else {
			fail(err.Error())
		}
		return
	}
	j.log(sshgw.StepOK, login.forgotten())
	if keys, err := s.emergencyKeys(bg); err == nil && len(keys) > 0 {
		if err := s.applyEmergency(id); err != nil {
			j.log(sshgw.StepWarn, i18n.T("emergency keys are not added: %s", err.Error()))
		} else {
			j.log(sshgw.StepOK, i18n.T("emergency keys added to authorized_keys of %s: %d", sshUser, len(keys)))
		}
	}

	if tokenHash != "" {
		if !res.AgentInstalled {
			_ = s.store.RevokeAgentToken(bg, tokenHash)
		} else {
			if !created {
				// The new agent replaced the old container.
				_ = s.store.RevokeOtherAgentTokens(bg, id, tokenHash)
			}
			j.log(sshgw.StepRun, i18n.T("waiting for the first agent heartbeat"))
			if s.waitHeartbeat(context.Background(), tokenHash) {
				j.log(sshgw.StepOK, i18n.T("the agent is online"))
			} else {
				j.log(sshgw.StepWarn, i18n.T("the agent has not sent metrics yet: check `docker logs srvmon-agent` on the server"))
			}
		}
	}
	_ = s.store.Audit(bg, ip, "server_onboard", req.Card.Name)
	s.log.Info("server onboarded", "server", req.Card.Name, "agent", res.AgentInstalled, "password_disabled", res.PasswordDisabled)
	j.log(sshgw.StepOK, i18n.T("done"))
	j.finish(true)
}

// waitHeartbeat waits for the first push made with the new agent's token;
// pushes of a replaced agent (other tokens) do not count.
func (s *Server) waitHeartbeat(ctx context.Context, tokenHash string) bool {
	ctx, cancel := context.WithTimeout(ctx, heartbeatWait)
	defer cancel()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		if ts, err := s.store.TokenLastSeen(ctx, tokenHash); err == nil && !ts.IsZero() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
}

func keyAAD(serverID int64) []byte { return []byte(fmt.Sprintf("srvmon:ssh-key:server:%d", serverID)) }

func (s *Server) handleOnboardStatus(w http.ResponseWriter, r *http.Request) {
	j := s.jobs.get(r.PathValue("job"))
	if j == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	j.mu.Lock()
	defer j.mu.Unlock()
	if from < 0 || from > len(j.lines) {
		from = len(j.lines)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines":     append([]jobLine{}, j.lines[from:]...),
		"next":      len(j.lines),
		"done":      j.done,
		"ok":        j.ok,
		"committed": j.committed,
		"server_id": j.serverID,
	})
}

func (s *Server) handleOnboardCancel(w http.ResponseWriter, r *http.Request) {
	j := s.jobs.get(r.PathValue("job"))
	if j == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	j.mu.Lock()
	committed := j.committed
	if !committed {
		j.cancel()
	}
	j.mu.Unlock()
	if committed {
		writeError(w, http.StatusConflict, "already_committed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	// A running job keeps the id and would later write to or delete a card
	// that reused it; cancel the job instead (it rolls back itself).
	if s.jobs.active(id) {
		writeError(w, http.StatusConflict, "onboarding")
		return
	}
	if err := s.store.DeleteServer(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.latest.Forget(id)
	s.alerts.Forget(id)
	_ = s.store.Audit(r.Context(), s.clientIP(r), "server_delete", strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
