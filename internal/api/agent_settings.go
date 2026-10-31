package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/store"
)

// AI settings live in the panel: provider, address, key, models. srvmon
// keeps them (the key sealed with the master key, one per provider) and
// hands them to srvmon-ai on every change and when srvmon-ai starts
// (GET /tools/ai-config). Without saved settings srvmon-ai falls back to its
// SRVMON_AI_* environment.

const (
	settingAI       = "ai.config"
	settingAIKeyPre = "ai.key."
	aiMaxModels     = 500
	aiKeyMax        = 1000
)

var (
	aiKeyAAD       = []byte("srvmon ai api key")
	aiProviderRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	aiThinking     = []string{"off", "minimal", "low", "medium", "high"}
	aiCustomAPIs   = []string{"openai-completions", "openai-responses", "anthropic-messages"}
	errAINoSecrets = errors.New("no master key")
)

// aiSettings is what srvmon stores; the key is kept apart, per provider.
type aiSettings struct {
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"base_url,omitempty"`
	API       string    `json:"api,omitempty"`
	Model     string    `json:"model"`
	Thinking  string    `json:"thinking"`
	Models    []string  `json:"models"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Server) loadAISettings(ctx context.Context) (aiSettings, bool, error) {
	var a aiSettings
	v, err := s.store.GetSetting(ctx, settingAI)
	if errors.Is(err, store.ErrNotFound) {
		return a, false, nil
	} else if err != nil {
		return a, false, err
	}
	if err := json.Unmarshal([]byte(v), &a); err != nil {
		return a, false, err
	}
	return a, true, nil
}

// aiKey returns the stored key of a provider ("" when none or unreadable).
func (s *Server) aiKey(ctx context.Context, provider string) string {
	if s.secrets == nil {
		return ""
	}
	v, err := s.store.GetSetting(ctx, settingAIKeyPre+provider)
	if err != nil || v == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return ""
	}
	pt, err := s.secrets.Open(raw, append(aiKeyAAD, provider...))
	if err != nil {
		s.log.Error("ai: cannot decrypt the API key; set it again in the panel", "provider", provider)
		return ""
	}
	return string(pt)
}

func (s *Server) setAIKey(ctx context.Context, provider, key string) error {
	if key == "" {
		return s.store.SetSetting(ctx, settingAIKeyPre+provider, "")
	}
	if s.secrets == nil {
		return errAINoSecrets
	}
	sealed, err := s.secrets.Seal([]byte(key), append(aiKeyAAD, provider...))
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, settingAIKeyPre+provider, base64.StdEncoding.EncodeToString(sealed))
}

// aiConfig is the full setup for srvmon-ai, key included.
func (s *Server) aiConfig(ctx context.Context) (aiagent.Config, bool, error) {
	a, ok, err := s.loadAISettings(ctx)
	if err != nil || !ok {
		return aiagent.Config{}, false, err
	}
	return aiagent.Config{
		Provider: a.Provider, BaseURL: a.BaseURL, API: a.API, Model: a.Model, Thinking: a.Thinking,
		Models: a.Models, APIKey: s.aiKey(ctx, a.Provider),
	}, true, nil
}

// pushAIConfig hands the saved setup to srvmon-ai.
func (s *Server) pushAIConfig(ctx context.Context) (aiagent.Status, error) {
	cfg, ok, err := s.aiConfig(ctx)
	if err != nil {
		return aiagent.Status{}, err
	}
	if !ok {
		return s.ai.Status(ctx)
	}
	return s.ai.Configure(ctx, cfg)
}

// toolAIConfig is srvmon-ai asking for its setup at start.
func (s *Server) toolAIConfig(w http.ResponseWriter, r *http.Request) {
	cfg, ok, err := s.aiConfig(r.Context())
	if err != nil {
		s.internalError(w, "ai config", err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

type aiSettingsView struct {
	aiSettings
	// Saved is false while srvmon-ai runs on its environment defaults.
	Saved   bool   `json:"saved"`
	KeySet  bool   `json:"key_set"`
	KeyHint string `json:"key_hint,omitempty"`
	// Keys lists providers that have a stored key.
	Keys   []string        `json:"keys"`
	Status *aiagent.Status `json:"status,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func keyHint(k string) string {
	if len(k) < 12 {
		return "задан"
	}
	return "…" + k[len(k)-4:]
}

func (s *Server) aiSettingsView(ctx context.Context) aiSettingsView {
	a, ok, err := s.loadAISettings(ctx)
	v := aiSettingsView{aiSettings: a, Saved: ok, Keys: []string{}}
	if err != nil {
		v.Error = "настройки повреждены: сохраните их заново"
	}
	if st, err := s.ai.Status(ctx); err == nil {
		v.Status = &st
		if !ok {
			// Show what srvmon-ai runs with now (environment defaults).
			v.Provider, v.Model = st.Provider, st.Model
		}
	} else {
		v.Error = "srvmon-ai недоступен"
	}
	if v.Thinking == "" {
		v.Thinking = "low"
	}
	if v.Models == nil {
		v.Models = []string{}
	}
	if k := s.aiKey(ctx, v.Provider); k != "" {
		v.KeySet, v.KeyHint = true, keyHint(k)
	}
	if keys, err := s.store.SettingsWithPrefix(ctx, settingAIKeyPre); err == nil {
		for k, val := range keys {
			if val != "" {
				v.Keys = append(v.Keys, strings.TrimPrefix(k, settingAIKeyPre))
			}
		}
		slices.Sort(v.Keys)
	}
	return v
}

func (s *Server) handleAISettings(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.aiSettingsView(r.Context()))
}

type aiSettingsInput struct {
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url"`
	API      string   `json:"api"`
	Model    string   `json:"model"`
	Thinking string   `json:"thinking"`
	Models   []string `json:"models"`
	// APIKey: absent keeps the stored key of the provider, "" removes it.
	APIKey *string `json:"api_key"`
}

func cleanBaseURL(raw string) (string, bool) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 500 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return raw, true
}

func validModelID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, c := range id {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

func (in *aiSettingsInput) validate() string {
	in.Provider = strings.TrimSpace(in.Provider)
	if !aiProviderRe.MatchString(in.Provider) {
		return "provider"
	}
	var ok bool
	if in.BaseURL, ok = cleanBaseURL(in.BaseURL); !ok {
		return "base_url"
	}
	if in.Provider == "custom" {
		if in.BaseURL == "" {
			return "base_url"
		}
		if !slices.Contains(aiCustomAPIs, in.API) {
			return "api"
		}
	} else {
		in.API = ""
	}
	in.Model = strings.TrimSpace(in.Model)
	if !validModelID(in.Model) {
		return "model"
	}
	if in.Thinking == "" {
		in.Thinking = "low"
	}
	if !slices.Contains(aiThinking, in.Thinking) {
		return "thinking"
	}
	if len(in.Models) > aiMaxModels {
		return "models"
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(in.Models)+1)
	for _, m := range append([]string{in.Model}, in.Models...) {
		m = strings.TrimSpace(m)
		if !validModelID(m) {
			return "models"
		}
		if !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}
	in.Models = models
	if in.APIKey != nil {
		k := strings.TrimSpace(*in.APIKey)
		if len(k) > aiKeyMax || strings.ContainsFunc(k, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
			return "api_key"
		}
		in.APIKey = &k
	}
	return ""
}

// aiSettingsBodyLimit fits the largest allowed model list (500 ids of up to
// 200 bytes, JSON-escaped) with room for the other fields. srvmon-ai accepts
// the same size on /v1/config.
const aiSettingsBodyLimit = 256 << 10

func (s *Server) handleAISaveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	var in aiSettingsInput
	if err := readJSONLimit(r, &in, aiSettingsBodyLimit); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if f := in.validate(); f != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation", "field": f})
		return
	}
	ctx := r.Context()
	if in.APIKey != nil {
		if err := s.setAIKey(ctx, in.Provider, *in.APIKey); err != nil {
			if errors.Is(err, errAINoSecrets) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation", "field": "api_key",
					"message": "нет мастер-ключа: ключ API негде хранить"})
				return
			}
			s.internalError(w, "ai key", err)
			return
		}
	}
	a := aiSettings{Provider: in.Provider, BaseURL: in.BaseURL, API: in.API, Model: in.Model, Thinking: in.Thinking,
		Models: in.Models, UpdatedAt: s.now().UTC()}
	b, _ := json.Marshal(a)
	if err := s.store.SetSetting(ctx, settingAI, string(b)); err != nil {
		s.internalError(w, "ai settings", err)
		return
	}
	detail := a.Provider + "/" + a.Model
	if in.APIKey != nil {
		detail += map[bool]string{true: ", ключ удалён", false: ", новый ключ"}[*in.APIKey == ""]
	}
	_ = s.store.Audit(ctx, s.clientIP(r), "ai_settings", detail)
	v := s.aiSettingsView(ctx)
	if st, err := s.pushAIConfig(ctx); err != nil {
		v.Error = "настройки сохранены, но srvmon-ai их не принял: " + err.Error()
	} else {
		v.Status = &st
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleAIProviders(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	list, err := s.ai.Providers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAIModels lists a provider's models. Without a key in the request
// the stored key of that provider is used, so the browser never gets it.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	var in aiSettingsInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	in.Model = "x" // not part of this request
	if f := in.validate(); f != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation", "field": f})
		return
	}
	req := aiagent.ModelsRequest{Provider: in.Provider, BaseURL: in.BaseURL, API: in.API}
	if in.APIKey != nil && *in.APIKey != "" {
		req.APIKey = *in.APIKey
	} else {
		req.APIKey = s.aiKey(r.Context(), in.Provider)
	}
	list, err := s.ai.Models(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	res, err := s.ai.Test(r.Context(), in.Model)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAgentChatModel sets the model of one chat ("" = default).
func (s *Server) handleAgentChatModel(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	in.Model = strings.TrimSpace(in.Model)
	if in.Model != "" {
		a, _, _ := s.loadAISettings(r.Context())
		if !slices.Contains(a.Models, in.Model) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "validation", "field": "model"})
			return
		}
	}
	if err := s.store.SetAgentChatModel(r.Context(), c.ID, in.Model); err != nil {
		s.internalError(w, "chat model", err)
		return
	}
	c.Model = in.Model
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), c))
}
