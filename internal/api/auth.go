package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	cookiePending = "srvmon_pending"
	cookieSession = "srvmon_session"
	pendingTTL    = 2 * time.Minute
)

type ctxKey int

const ctxSession ctxKey = 1

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1, HttpOnly: true,
		Secure: s.cfg.SecureCookie, SameSite: http.SameSiteStrictMode})
}

// authFailure records a failed attempt for the limiter, the audit table and
// the fail2ban log (filter: deploy/fail2ban/filter.d/srvmon-auth.conf).
func (s *Server) authFailure(ctx context.Context, ip, stage string) {
	s.limiter.Fail(ip)
	fmt.Fprintf(s.authLog, "%s srvmon auth failure ip=%s stage=%s\n", s.now().UTC().Format(time.RFC3339), ip, stage)
	if err := s.store.Audit(ctx, ip, "auth_failure", stage); err != nil {
		s.log.Error("audit", "err", err)
	}
}

func (s *Server) rejectIfBlocked(w http.ResponseWriter, ip string) bool {
	blocked, wait := s.limiter.Blocked(ip)
	if !blocked {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts")
	return true
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.rejectIfBlocked(w, ip) {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	hash, err := s.store.GetSetting(r.Context(), store.KeyPasswordHash)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusServiceUnavailable, "not_initialized")
		return
	} else if err != nil {
		s.log.Error("get password hash", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil {
		s.log.Error("verify password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ok {
		s.authFailure(r.Context(), ip, "password")
		writeError(w, http.StatusUnauthorized, "invalid_password")
		return
	}
	token, err := auth.NewToken("p_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := s.store.CreateSession(r.Context(), auth.HashToken(token), store.SessionPending, ip, pendingTTL); err != nil {
		s.log.Error("create pending session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.setCookie(w, cookiePending, token, "/api/auth", pendingTTL)
	writeJSON(w, http.StatusOK, map[string]string{"next": "totp"})
}

func (s *Server) handleTOTP(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.rejectIfBlocked(w, ip) {
		return
	}
	c, err := r.Cookie(cookiePending)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "password_required")
		return
	}
	pendingHash := auth.HashToken(c.Value)
	if kind, err := s.store.SessionKind(r.Context(), pendingHash); err != nil || kind != store.SessionPending {
		s.clearCookie(w, cookiePending, "/api/auth")
		writeError(w, http.StatusUnauthorized, "password_required")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ok, method, err := s.checkSecondFactor(r.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		s.log.Error("second factor", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ok {
		s.authFailure(r.Context(), ip, "totp")
		writeError(w, http.StatusUnauthorized, "invalid_code")
		return
	}
	_ = s.store.DeleteSession(r.Context(), pendingHash)
	token, err := auth.NewToken("s_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := s.store.CreateSession(r.Context(), auth.HashToken(token), store.SessionFull, ip, s.cfg.SessionTTL); err != nil {
		s.log.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.limiter.Reset(ip)
	_ = s.store.Audit(r.Context(), ip, "login", method)
	s.log.Info("login", "ip", ip, "method", method)
	s.clearCookie(w, cookiePending, "/api/auth")
	s.setCookie(w, cookieSession, token, "/", s.cfg.SessionTTL)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// checkSecondFactor accepts a 6-digit TOTP code or a one-time recovery code.
func (s *Server) checkSecondFactor(ctx context.Context, code string) (bool, string, error) {
	if len(code) == 6 {
		s.totpMu.Lock()
		defer s.totpMu.Unlock()
		secret, err := s.store.GetSetting(ctx, store.KeyTOTPSecret)
		if err != nil {
			return false, "", err
		}
		var last int64
		if v, err := s.store.GetSetting(ctx, store.KeyTOTPLastStep); err == nil {
			last, _ = strconv.ParseInt(v, 10, 64)
		}
		step, ok := auth.VerifyTOTP(secret, code, s.now(), last)
		if !ok {
			return false, "", nil
		}
		return true, "totp", s.store.SetSetting(ctx, store.KeyTOTPLastStep, strconv.FormatInt(step, 10))
	}
	if code == "" {
		return false, "", nil
	}
	ok, err := s.store.UseRecoveryCode(ctx, auth.HashToken(code))
	return ok, "recovery", err
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieSession); err == nil {
		_ = s.store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	s.clearCookie(w, cookieSession, "/")
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	_, err := s.store.GetSetting(r.Context(), store.KeyPasswordHash)
	initialized := err == nil
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": s.sessionValid(r),
		"initialized":   initialized,
		"version":       s.version,
	})
}

func (s *Server) sessionValid(r *http.Request) bool {
	c, err := r.Cookie(cookieSession)
	if err != nil {
		return false
	}
	kind, err := s.store.SessionKind(r.Context(), auth.HashToken(c.Value))
	return err == nil && kind == store.SessionFull
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sessionValid(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxSession, true)))
	})
}
