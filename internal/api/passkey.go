package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// Sign-in with passkeys (WebAuthn: Face ID, Touch ID, Windows Hello, a
// phone or a security key). It is off until the owner adds a passkey in
// the settings, and the password with TOTP always keeps working.
//
// WebAuthn binds a passkey to a domain (the RP ID), so it works only over
// HTTPS on a domain name (or http://localhost), never on a bare IP address.
// The RP ID is the host the panel is opened at, taken from the browser's
// Origin header (checked against SRVMON_PUBLIC_ORIGIN or Host by
// checkOrigin), never a fixed name: each installation has its own.

const (
	cookieWebAuthn    = "srvmon_webauthn"
	passkeyTTL        = 2 * time.Minute
	passkeyBodyLimit  = 64 << 10
	settingPasskeyUID = "passkey.user_handle"
	maxPasskeyName    = 64
	maxCeremonies     = 256
	maxCeremoniesIP   = 4
)

func (s *Server) passkeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/passkey/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/auth/passkey/finish", s.handlePasskeyLoginFinish)
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/passkeys", s.handlePasskeys)
	h("POST /api/passkeys/begin", s.handlePasskeyAddBegin)
	h("POST /api/passkeys/finish", s.handlePasskeyAddFinish)
	h("DELETE /api/passkeys/{id}", s.handlePasskeyDelete)
}

// passkeyEnvProblem says why passkeys cannot work at this address, or "".
func passkeyEnvProblem(scheme, host string) string {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return "passkey_no_origin"
	}
	if net.ParseIP(host) != nil {
		return "passkey_ip"
	}
	if scheme != "https" && host != "localhost" && !strings.HasSuffix(host, ".localhost") {
		return "passkey_insecure"
	}
	if protocol.ValidateRPID(host) != nil {
		return "passkey_bad_domain"
	}
	return ""
}

// passkeyOrigin returns the browser's origin and its host as the RP ID, or
// an error code that tells the owner why passkeys are not available here.
func passkeyOrigin(r *http.Request) (origin, rpID, problem string) {
	o := r.Header.Get("Origin")
	u, err := url.Parse(o)
	if o == "" || o == "null" || err != nil || u.Host == "" {
		return "", "", "passkey_no_origin"
	}
	host := strings.ToLower(u.Hostname())
	if p := passkeyEnvProblem(u.Scheme, host); p != "" {
		return "", "", p
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), host, ""
}

// panelHost is the host the panel is opened at, for GET requests that
// carry no Origin.
func (s *Server) panelHost(r *http.Request) string {
	if s.cfg.PublicOrigin != "" {
		if u, err := url.Parse(s.cfg.PublicOrigin); err == nil && u.Hostname() != "" {
			return strings.ToLower(u.Hostname())
		}
	}
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// passkeyOffered reports whether the sign-in page should show the passkey
// button: a passkey exists for the host the panel is opened at.
func (s *Server) passkeyOffered(r *http.Request) bool {
	host := s.panelHost(r)
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	keys, err := s.store.ListPasskeys(r.Context())
	if err != nil {
		return false
	}
	for _, k := range keys {
		if k.RPID == host {
			return true
		}
	}
	return false
}

func newWebAuthn(origin, rpID string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                  rpID,
		RPDisplayName:         "srvmon",
		RPOrigins:             []string{origin},
		AttestationPreference: protocol.PreferNoAttestation,
	})
}

// passkeyUser is the panel's only user as WebAuthn sees it, with the
// passkeys of one RP ID.
type passkeyUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.id }
func (u *passkeyUser) WebAuthnName() string                       { return "owner" }
func (u *passkeyUser) WebAuthnDisplayName() string                { return "srvmon owner" }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (u *passkeyUser) exclusions() []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for _, c := range u.creds {
		out = append(out, c.Descriptor())
	}
	return out
}

var errNoPasskeys = errors.New("no passkeys")

// passkeyUser loads the user handle (made on first use when create is set)
// and the passkeys of rpID.
func (s *Server) passkeyUser(ctx context.Context, rpID string, create bool) (*passkeyUser, error) {
	s.passkeyMu.Lock()
	defer s.passkeyMu.Unlock()
	v, err := s.store.GetSetting(ctx, settingPasskeyUID)
	if errors.Is(err, store.ErrNotFound) {
		if !create {
			return nil, errNoPasskeys
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		v = base64.RawURLEncoding.EncodeToString(b)
		if err := s.store.SetSetting(ctx, settingPasskeyUID, v); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	id, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil, err
	}
	u := &passkeyUser{id: id}
	keys, err := s.store.ListPasskeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.RPID != rpID {
			continue
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(k.Data), &c); err != nil {
			s.log.Error("passkey: bad stored credential", "id", k.ID, "err", err)
			continue
		}
		u.creds = append(u.creds, c)
	}
	if len(u.creds) == 0 && !create {
		return nil, errNoPasskeys
	}
	return u, nil
}

// ceremony is a started WebAuthn sign-in or registration, waiting for the
// browser's answer. Ceremonies live in memory for passkeyTTL.
type ceremony struct {
	kind    string // "login" | "register"
	ip      string
	origin  string
	rpID    string
	name    string
	data    webauthn.SessionData
	expires time.Time
}

type ceremonies struct {
	mu sync.Mutex
	m  map[string]ceremony // by token hash
}

func newCeremonies() *ceremonies { return &ceremonies{m: map[string]ceremony{}} }

func (c *ceremonies) put(now time.Time, cer ceremony) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	perIP := 0
	for k, v := range c.m {
		if now.After(v.expires) {
			delete(c.m, k)
		} else if v.ip == cer.ip {
			perIP++
		}
	}
	if len(c.m) >= maxCeremonies || perIP >= maxCeremoniesIP {
		return "", errTooManyJobs
	}
	token, err := auth.NewToken("w_")
	if err != nil {
		return "", err
	}
	cer.expires = now.Add(passkeyTTL)
	c.m[auth.HashToken(token)] = cer
	return token, nil
}

// take removes and returns a live ceremony of the given kind.
func (c *ceremonies) take(now time.Time, token, kind string) (ceremony, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := auth.HashToken(token)
	cer, ok := c.m[h]
	if !ok {
		return cer, false
	}
	delete(c.m, h)
	return cer, cer.kind == kind && now.Before(cer.expires)
}

func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, kind string) (ceremony, bool) {
	c, err := r.Cookie(cookieWebAuthn)
	if err != nil {
		return ceremony{}, false
	}
	s.clearCookie(w, cookieWebAuthn, "/api")
	return s.passkeys.take(s.now(), c.Value, kind)
}

func (s *Server) startCeremony(w http.ResponseWriter, cer ceremony) bool {
	token, err := s.passkeys.put(s.now(), cer)
	if errors.Is(err, errTooManyJobs) {
		writeError(w, http.StatusTooManyRequests, "busy")
		return false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return false
	}
	s.setCookie(w, cookieWebAuthn, token, "/api", passkeyTTL)
	return true
}

// --- sign-in ---

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	origin, rpID, problem := passkeyOrigin(r)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	u, err := s.passkeyUser(r.Context(), rpID, false)
	if errors.Is(err, errNoPasskeys) {
		writeError(w, http.StatusNotFound, "no_passkeys")
		return
	} else if err != nil {
		s.internalError(w, "passkey user", err)
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		s.internalError(w, "webauthn", err)
		return
	}
	assertion, data, err := wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.internalError(w, "passkey begin login", err)
		return
	}
	if !s.startCeremony(w, ceremony{kind: "login", ip: s.clientIP(r), origin: origin, rpID: rpID, data: *data}) {
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	att, ok := s.reserveAttempt(w, ip)
	if !ok {
		return
	}
	defer att.done()
	cer, ok := s.takeCeremony(w, r, "login")
	if !ok {
		writeError(w, http.StatusUnauthorized, "passkey_expired")
		return
	}
	origin, rpID, _ := passkeyOrigin(r)
	if origin != cer.origin || rpID != cer.rpID {
		writeError(w, http.StatusBadRequest, "bad_origin")
		return
	}
	fail := func(err error) {
		s.log.Warn("passkey sign-in failed", "ip", ip, "err", err)
		att.fail()
		s.authFailure(r.Context(), ip, "passkey")
		writeError(w, http.StatusUnauthorized, "invalid_passkey")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(io.LimitReader(r.Body, passkeyBodyLimit))
	if err != nil {
		fail(err)
		return
	}
	u, err := s.passkeyUser(r.Context(), rpID, false)
	if errors.Is(err, errNoPasskeys) {
		fail(err)
		return
	} else if err != nil {
		s.internalError(w, "passkey user", err)
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		s.internalError(w, "webauthn", err)
		return
	}
	cred, err := wa.ValidateLogin(u, cer.data, parsed)
	if err != nil {
		fail(err)
		return
	}
	if cred.Authenticator.CloneWarning {
		fail(errors.New("signature counter went back: the passkey may be cloned"))
		return
	}
	b, err := json.Marshal(cred)
	if err != nil {
		s.internalError(w, "passkey encode", err)
		return
	}
	if err := s.store.UsePasskey(r.Context(), base64.RawURLEncoding.EncodeToString(cred.ID), string(b)); err != nil {
		// Removed between begin and finish.
		fail(err)
		return
	}
	s.startSession(w, r, ip, "passkey")
}

// --- settings ---

type passkeyView struct {
	store.Passkey
	Here bool `json:"here"` // made for the address the panel is opened at
}

func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListPasskeys(r.Context())
	if err != nil {
		s.internalError(w, "list passkeys", err)
		return
	}
	host := s.panelHost(r)
	out := make([]passkeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, passkeyView{Passkey: k, Here: k.RPID == host})
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out, "host": host, "max": store.MaxPasskeys})
}

// reauth checks a fresh TOTP (or recovery) code: adding or removing a
// passkey changes how the panel is entered, a session alone is not enough.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request, code, stage string) bool {
	ip := s.clientIP(r)
	att, ok := s.reserveAttempt(w, ip)
	if !ok {
		return false
	}
	defer att.done()
	ok, _, err := s.checkSecondFactor(r.Context(), strings.TrimSpace(code))
	if err != nil {
		s.internalError(w, "second factor", err)
		return false
	}
	if !ok {
		att.fail()
		s.authFailure(r.Context(), ip, stage)
		writeError(w, http.StatusUnauthorized, "invalid_code")
		return false
	}
	return true
}

func (s *Server) handlePasskeyAddBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	origin, rpID, problem := passkeyOrigin(r)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) > maxPasskeyName {
		name = string([]rune(name)[:maxPasskeyName])
	}
	if name == "" {
		name = "passkey"
	}
	if !s.reauth(w, r, req.Code, "passkey_reauth") {
		return
	}
	if keys, err := s.store.ListPasskeys(r.Context()); err != nil {
		s.internalError(w, "list passkeys", err)
		return
	} else if len(keys) >= store.MaxPasskeys {
		writeError(w, http.StatusConflict, "too_many_passkeys")
		return
	}
	u, err := s.passkeyUser(r.Context(), rpID, true)
	if err != nil {
		s.internalError(w, "passkey user", err)
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		s.internalError(w, "webauthn", err)
		return
	}
	creation, data, err := wa.BeginRegistration(u,
		webauthn.WithExclusions(u.exclusions()),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		s.internalError(w, "passkey begin registration", err)
		return
	}
	if !s.startCeremony(w, ceremony{kind: "register", ip: s.clientIP(r), origin: origin, rpID: rpID, name: name, data: *data}) {
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

func (s *Server) handlePasskeyAddFinish(w http.ResponseWriter, r *http.Request) {
	cer, ok := s.takeCeremony(w, r, "register")
	if !ok {
		writeError(w, http.StatusBadRequest, "passkey_expired")
		return
	}
	origin, rpID, _ := passkeyOrigin(r)
	if origin != cer.origin || rpID != cer.rpID {
		writeError(w, http.StatusBadRequest, "bad_origin")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(io.LimitReader(r.Body, passkeyBodyLimit))
	if err != nil {
		s.log.Warn("passkey registration: parse", "err", err)
		writeError(w, http.StatusBadRequest, "invalid_passkey")
		return
	}
	u, err := s.passkeyUser(r.Context(), rpID, true)
	if err != nil {
		s.internalError(w, "passkey user", err)
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		s.internalError(w, "webauthn", err)
		return
	}
	cred, err := wa.CreateCredential(u, cer.data, parsed)
	if err != nil {
		s.log.Warn("passkey registration: verify", "err", err)
		writeError(w, http.StatusBadRequest, "invalid_passkey")
		return
	}
	b, err := json.Marshal(cred)
	if err != nil {
		s.internalError(w, "passkey encode", err)
		return
	}
	id, err := s.store.AddPasskey(r.Context(), store.Passkey{
		CredID: base64.RawURLEncoding.EncodeToString(cred.ID), RPID: rpID, Name: cer.name, Data: string(b),
	})
	switch {
	case errors.Is(err, store.ErrTooManyPasskeys):
		writeError(w, http.StatusConflict, "too_many_passkeys")
		return
	case err != nil && strings.Contains(err.Error(), "UNIQUE"):
		writeError(w, http.StatusConflict, "passkey_exists")
		return
	case err != nil:
		s.internalError(w, "add passkey", err)
		return
	}
	ip := s.clientIP(r)
	_ = s.store.Audit(r.Context(), ip, "passkey_add", cer.name+" @ "+rpID)
	s.log.Info("passkey added", "ip", ip, "rp_id", rpID)
	s.alerts.Event(r.Context(), store.KindLogin, 0, "", ip,
		i18n.T("a passkey %q for %s was added to the panel (IP %s)", cer.name, rpID, ip))
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.reauth(w, r, req.Code, "passkey_reauth") {
		return
	}
	p, err := s.store.DeletePasskey(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "delete passkey", err)
		return
	}
	ip := s.clientIP(r)
	_ = s.store.Audit(r.Context(), ip, "passkey_delete", p.Name+" @ "+p.RPID)
	s.log.Info("passkey removed", "ip", ip, "rp_id", p.RPID)
	s.alerts.Event(r.Context(), store.KindLogin, 0, "", ip,
		i18n.T("the passkey %q for %s was removed from the panel (IP %s)", p.Name, p.RPID, ip))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
