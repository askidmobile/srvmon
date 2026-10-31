package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// loginSSH logs in and gives the server a master key for SSH secrets.
func (e *env) loginSSH(t *testing.T) {
	t.Helper()
	e.login(t)
	key := make([]byte, 32)
	rand.Read(key)
	e.srv.secrets, _ = secret.New(key)
}

func (e *env) postJSON(t *testing.T, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := e.client.Post(e.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestStage3RoutesRequireSession(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/servers/probe"},
		{"POST", "/api/servers/onboard"},
		{"GET", "/api/onboard/x"},
		{"DELETE", "/api/servers/1"},
		{"GET", "/api/servers/1/terminal"},
	} {
		req, _ := http.NewRequest(c.method, e.ts.URL+c.path, strings.NewReader("{}"))
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without session: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestOnboardValidation(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	key, _ := sshgw.GenerateKey("")
	valid := func() onboardReq {
		return onboardReq{
			Card:     cardInput{Name: "shop-db", Host: "127.0.0.1", Port: 1},
			User:     "root",
			Password: "pw",
			HostKey:  key.Public,
		}
	}
	cases := map[string]func(*onboardReq){
		"bad_name":          func(r *onboardReq) { r.Card.Name = "shop db; rm" },
		"bad_host":          func(r *onboardReq) { r.Card.Host = "a b" },
		"bad_port":          func(r *onboardReq) { r.Card.Port = 70000 },
		"bad_date":          func(r *onboardReq) { r.Card.NextPayment = "03.11.2026" },
		"bad_url":           func(r *onboardReq) { r.Card.PanelURL = "javascript:alert(1)" },
		"bad_period":        func(r *onboardReq) { r.Card.Period = "week" },
		"password_required": func(r *onboardReq) { r.Password = "" },
		"bad_user":          func(r *onboardReq) { r.User = "" },
		"bad_host_key":      func(r *onboardReq) { r.HostKey = "nope" },
	}
	for want, mut := range cases {
		req := valid()
		mut(&req)
		var out map[string]string
		if code := e.postJSON(t, "/api/servers/onboard", req, &out); code != http.StatusBadRequest || out["error"] != want {
			t.Errorf("%s: got %d %v", want, code, out)
		}
	}
	if list, _ := e.st.ListServers(context.Background()); len(list) != 0 {
		t.Fatalf("invalid requests must not create servers: %v", list)
	}

	if _, err := e.st.CreateServer(context.Background(), store.Server{Name: "taken"}); err != nil {
		t.Fatal(err)
	}
	req := valid()
	req.Card.Name = "taken"
	var out map[string]string
	if code := e.postJSON(t, "/api/servers/onboard", req, &out); code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %v", code, out)
	}
}

func TestOnboardFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	key, _ := sshgw.GenerateKey("")
	req := onboardReq{
		// Port 1 on localhost: connection refused right away.
		Card: cardInput{Name: "gone", Host: "127.0.0.1", Port: 1, Tags: []string{"#db", "db", " x "},
			NextPayment: "2026-11-03", Period: "month"},
		User: "root", Password: "secret-pw", HostKey: key.Public, InstallAgent: true,
	}
	var started struct {
		Job      string `json:"job"`
		ServerID int64  `json:"server_id"`
	}
	if code := e.postJSON(t, "/api/servers/onboard", req, &started); code != http.StatusAccepted || started.Job == "" {
		t.Fatalf("onboard: %d %+v", code, started)
	}
	var st struct {
		Lines []jobLine `json:"lines"`
		Done  bool      `json:"done"`
		OK    bool      `json:"ok"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for !st.Done && time.Now().Before(deadline) {
		resp := e.get(t, "/api/onboard/"+started.Job)
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	if !st.Done || st.OK {
		t.Fatalf("job should fail: %+v", st)
	}
	b, _ := json.Marshal(st.Lines)
	if strings.Contains(string(b), "secret-pw") {
		t.Fatal("password leaked into the log")
	}
	if list, _ := e.st.ListServers(context.Background()); len(list) != 0 {
		t.Fatalf("failed onboarding must remove the new card: %+v", list)
	}
}

func TestTerminalChecks(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "nokey"})

	get := func(path, origin string) int {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	path := "/api/servers/" + strconv.FormatInt(id, 10) + "/terminal"
	if c := get(path, ""); c != http.StatusForbidden {
		t.Errorf("no origin: %d", c)
	}
	if c := get(path, "https://evil.example"); c != http.StatusForbidden {
		t.Errorf("cross-site origin: %d", c)
	}
	if c := get(path, e.ts.URL); c != http.StatusNotFound {
		t.Errorf("server without key: %d", c)
	}
	if c := get(path+"?container=a%20b%27", e.ts.URL); c != http.StatusBadRequest {
		t.Errorf("bad container name: %d", c)
	}
}

func TestCardNormalize(t *testing.T) {
	c := cardInput{Name: " a ", Host: "[::1]", Tags: []string{"#db", "db", "", "web"}, Currency: "eur"}
	if code := c.normalize(); code != "" {
		t.Fatal(code)
	}
	if c.Name != "a" || c.Host != "::1" || c.Port != 22 || c.Currency != "EUR" || strings.Join(c.Tags, ",") != "db,web" {
		t.Fatalf("normalized: %+v", c)
	}
}

func TestExistingServerKeepsAddressOnFailure(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "old", Host: "10.0.0.1", Port: 22})
	key, _ := sshgw.GenerateKey("")
	req := onboardReq{ServerID: id, Card: cardInput{Name: "old", Host: "127.0.0.1", Port: 1},
		User: "root", Password: "x", HostKey: key.Public}
	var started struct{ Job string }
	if code := e.postJSON(t, "/api/servers/onboard", req, &started); code != http.StatusAccepted {
		t.Fatalf("onboard: %d", code)
	}
	deadline := time.Now().Add(20 * time.Second)
	for e.srv.jobs.active(id) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	list, _ := e.st.ListServers(ctx)
	if len(list) != 1 || list[0].Host != "10.0.0.1" || list[0].Port != 22 {
		t.Fatalf("existing card must keep its address and stay: %+v", list)
	}
}

func TestJobsOneActivePerServer(t *testing.T) {
	js := &jobs{m: map[string]*job{}}
	if _, err := js.add(&job{serverID: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.add(&job{serverID: 5}); !errors.Is(err, errServerBusy) {
		t.Fatalf("second job for the same server: %v", err)
	}
	if _, err := js.add(&job{serverID: 6}); err != nil {
		t.Fatalf("other server: %v", err)
	}
}

func TestDeleteRefusedDuringOnboarding(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "busy"})
	if _, err := e.srv.jobs.add(&job{serverID: id, cancel: func() {}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("DELETE", e.ts.URL+"/api/servers/"+strconv.FormatInt(id, 10), nil)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete during onboarding: %d", resp.StatusCode)
	}
}

func TestLoginFor(t *testing.T) {
	s := &Server{setupKeys: &setupKeys{m: map[string]setupKey{}}}
	plain, _ := sshgw.GenerateKey("")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("open sesame"))
	encrypted := string(pem.EncodeToMemory(block))
	panel, _ := sshgw.GenerateKey("")
	setupID, _ := s.setupKeys.add(panel)

	cases := []struct {
		req  onboardReq
		want string
	}{
		{onboardReq{}, "password_required"},
		{onboardReq{Password: "pw"}, ""},
		{onboardReq{Method: "key"}, "private_key_required"},
		{onboardReq{Method: "key", PrivateKey: plain.Public}, "public_key_given"},
		{onboardReq{Method: "key", PrivateKey: "garbage"}, "bad_private_key"},
		{onboardReq{Method: "key", PrivateKey: string(plain.Private)}, ""},
		{onboardReq{Method: "key", PrivateKey: encrypted}, "passphrase_required"},
		{onboardReq{Method: "key", PrivateKey: encrypted, Passphrase: "nope"}, "bad_passphrase"},
		{onboardReq{Method: "key", PrivateKey: encrypted, Passphrase: "open sesame"}, ""},
		{onboardReq{Method: "panel_key", SetupKey: "k_missing"}, "setup_key_expired"},
		{onboardReq{Method: "panel_key", SetupKey: setupID}, ""},
		{onboardReq{Method: "telnet"}, "bad_method"},
	}
	for i, c := range cases {
		l, code := s.loginFor(&c.req)
		if code != c.want {
			t.Errorf("case %d: got %q want %q", i, code, c.want)
		}
		if code == "" && c.req.Method != "" && c.req.Method != "password" && l.signer == nil {
			t.Errorf("case %d: no signer", i)
		}
		if c.req.PrivateKey != "" || c.req.Passphrase != "" {
			t.Errorf("case %d: key material kept in the request", i)
		}
	}
	l, _ := s.loginFor(&onboardReq{Method: "panel_key", SetupKey: setupID})
	if l.panelKey == nil || l.panelKey.Public != panel.Public {
		t.Fatal("panel key not used as the srvmon key")
	}
	s.setupKeys.drop(setupID)
	if _, code := s.loginFor(&onboardReq{Method: "panel_key", SetupKey: setupID}); code != "setup_key_expired" {
		t.Fatal("dropped setup key still usable")
	}
}

func TestSetupKeyEndpoint(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	var out map[string]string
	if code := e.postJSON(t, "/api/servers/setup-key", map[string]any{}, &out); code != http.StatusOK {
		t.Fatalf("setup-key: %d", code)
	}
	if out["id"] == "" || !strings.HasPrefix(out["public_key"], "ssh-ed25519 ") || !strings.Contains(out["command"], out["public_key"]) {
		t.Fatalf("setup-key: %v", out)
	}
	if strings.Contains(fmt.Sprint(out), "PRIVATE") {
		t.Fatal("private key leaked")
	}
}

// A large private key plus a full card must fit into the onboarding body.
func TestOnboardBodyFitsLargeKey(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	hostKey, _ := sshgw.GenerateKey("")
	req := onboardReq{
		Card: cardInput{Name: "big", Host: "127.0.0.1", Port: 1, Description: strings.Repeat("d", 500)},
		User: "root", Method: "key", HostKey: hostKey.Public,
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", 12<<10) + "\n-----END OPENSSH PRIVATE KEY-----\n",
	}
	var out map[string]string
	code := e.postJSON(t, "/api/servers/onboard", req, &out)
	// The fake key is rejected by the key parser, not cut off by the body limit.
	if code != http.StatusBadRequest || out["error"] != "bad_private_key" {
		t.Fatalf("got %d %v", code, out)
	}
}
