package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/store"
)

// softKey is a software authenticator: an ES256 passkey that answers the
// panel's WebAuthn ceremonies the way a browser would.
type softKey struct {
	priv    *ecdsa.PrivateKey
	id      []byte
	rpID    string
	origin  string
	counter uint32
	userID  string
}

func newSoftKey(t *testing.T, origin, rpID string) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: priv, id: id, rpID: rpID, origin: origin}
}

var b64 = base64.RawURLEncoding

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func (k *softKey) authData(flags byte, extra []byte) []byte {
	h := sha256.Sum256([]byte(k.rpID))
	b := append([]byte{}, h[:]...)
	b = append(b, flags)
	b = binary.BigEndian.AppendUint32(b, k.counter)
	return append(b, extra...)
}

// create answers navigator.credentials.create().
func (k *softKey) create(t *testing.T, options []byte) []byte {
	t.Helper()
	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &opts); err != nil {
		t.Fatalf("creation options: %v: %s", err, options)
	}
	k.userID = opts.PublicKey.User.ID
	x := k.priv.PublicKey.X.FillBytes(make([]byte, 32))
	y := k.priv.PublicKey.Y.FillBytes(make([]byte, 32))
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	cred := make([]byte, 16) // AAGUID
	cred = binary.BigEndian.AppendUint16(cred, uint16(len(k.id)))
	cred = append(cred, k.id...)
	cred = append(cred, cose...)
	att, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(0x45, cred), // UP | UV | AT
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", opts.PublicKey.Challenge, k.origin)),
			"attestationObject": b64.EncodeToString(att),
		},
	})
	return b
}

// get answers navigator.credentials.get().
func (k *softKey) get(t *testing.T, options []byte) []byte {
	t.Helper()
	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &opts); err != nil {
		t.Fatalf("request options: %v: %s", err, options)
	}
	cd := clientData("webauthn.get", opts.PublicKey.Challenge, k.origin)
	ad := k.authData(0x05, nil) // UP | UV
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        k.userID,
		},
	})
	return b
}

const testOrigin = "https://mon.example.test"

// call sends a request from the browser at origin and returns status and body.
func (e *env) call(t *testing.T, method, path, origin string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func jsonBody(v any) []byte { b, _ := json.Marshal(v); return b }

// freshCode returns the current TOTP code, forgetting that it was used.
func (e *env) freshCode(t *testing.T) string {
	t.Helper()
	_ = e.st.SetSetting(context.Background(), store.KeyTOTPLastStep, "0")
	code, _ := auth.TOTPCode(e.secret, auth.TOTPStep(time.Now()))
	return code
}

func (e *env) passkeyOffered(t *testing.T) bool {
	t.Helper()
	r := e.get(t, "/api/auth/me")
	defer r.Body.Close()
	var me struct {
		Passkey bool `json:"passkey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&me)
	return me.Passkey
}

func TestPasskeyFlow(t *testing.T) {
	logs := &bytes.Buffer{}
	e := newEnv(t, func(o *Options) {
		o.Config.PublicOrigin = testOrigin
		o.Log = slog.New(slog.NewTextHandler(logs, nil))
	})
	if e.passkeyOffered(t) {
		t.Fatal("passkey offered before any was added")
	}
	if c, _ := e.call(t, "POST", "/api/auth/passkey/begin", testOrigin, nil); c != http.StatusNotFound {
		t.Fatalf("sign-in begin without passkeys: %d", c)
	}
	e.login(t)
	key := newSoftKey(t, testOrigin, "mon.example.test")

	// Adding needs a fresh code, and a wrong one is a failure for fail2ban.
	if c, _ := e.call(t, "POST", "/api/passkeys/begin", testOrigin, jsonBody(map[string]string{"code": "000000"})); c != http.StatusUnauthorized {
		t.Fatalf("add with a wrong code: %d", c)
	}
	if !strings.Contains(e.authLog.String(), "stage=passkey_reauth") {
		t.Fatalf("wrong reauth code not logged for fail2ban:\n%s", e.authLog)
	}
	c, opts := e.call(t, "POST", "/api/passkeys/begin", testOrigin, jsonBody(map[string]string{"code": e.freshCode(t), "name": "MacBook"}))
	if c != http.StatusOK {
		t.Fatalf("add begin: %d %s", c, opts)
	}
	if !strings.Contains(string(opts), `"userVerification":"required"`) {
		t.Errorf("user verification is not required: %s", opts)
	}
	if c, b := e.call(t, "POST", "/api/passkeys/finish", testOrigin, key.create(t, opts)); c != http.StatusOK {
		t.Fatalf("add finish: %d %s", c, b)
	}
	_, list := e.call(t, "GET", "/api/passkeys", "", nil)
	if !strings.Contains(string(list), `"name":"MacBook"`) || !strings.Contains(string(list), `"here":true`) ||
		!strings.Contains(string(list), `"rp_id":"mon.example.test"`) {
		t.Fatalf("list: %s", list)
	}
	if strings.Contains(string(list), "publicKey") {
		t.Fatalf("list leaks credential data: %s", list)
	}

	// Sign in with the passkey alone.
	e.post(t, "/api/auth/logout", nil)
	if !e.passkeyOffered(t) {
		t.Fatal("passkey not offered on the sign-in page")
	}
	signIn := func() (int, []byte) {
		key.counter++
		c, opts := e.call(t, "POST", "/api/auth/passkey/begin", testOrigin, nil)
		if c != http.StatusOK {
			t.Fatalf("sign-in begin: %d %s", c, opts)
		}
		return e.call(t, "POST", "/api/auth/passkey/finish", testOrigin, key.get(t, opts))
	}
	if c, b := signIn(); c != http.StatusOK {
		t.Fatalf("passkey sign-in: %d %s", c, b)
	}
	if r := e.get(t, "/api/servers"); r.StatusCode != http.StatusOK {
		t.Fatalf("servers after passkey sign-in: %d", r.StatusCode)
	}

	// A counter that goes back means a cloned key: refused and logged.
	e.post(t, "/api/auth/logout", nil)
	key.counter--
	if c, _ := signIn(); c != http.StatusUnauthorized {
		t.Fatalf("cloned passkey accepted: %d", c)
	}
	if !strings.Contains(logs.String(), "may be cloned") {
		t.Errorf("refused for another reason than the counter:\n%s", logs)
	}
	if !strings.Contains(e.authLog.String(), "stage=passkey\n") {
		t.Fatalf("failed passkey sign-in not logged for fail2ban:\n%s", e.authLog)
	}

	// Another key (not registered) is refused.
	other := newSoftKey(t, testOrigin, "mon.example.test")
	other.userID = key.userID
	_, opts = e.call(t, "POST", "/api/auth/passkey/begin", testOrigin, nil)
	if c, _ := e.call(t, "POST", "/api/auth/passkey/finish", testOrigin, other.get(t, opts)); c != http.StatusUnauthorized {
		t.Fatalf("unknown passkey accepted: %d", c)
	}
	// A finish without a begin has nothing to answer.
	if c, _ := e.call(t, "POST", "/api/auth/passkey/finish", testOrigin, key.get(t, opts)); c != http.StatusUnauthorized {
		t.Fatalf("finish without begin: %d", c)
	}

	// Removing needs a fresh code too; then the sign-in page has no passkey.
	key.counter += 5
	if c, b := signIn(); c != http.StatusOK {
		t.Fatalf("sign-in: %d %s", c, b)
	}
	var lst struct {
		Passkeys []struct {
			ID int64 `json:"id"`
		} `json:"passkeys"`
	}
	_, list = e.call(t, "GET", "/api/passkeys", "", nil)
	_ = json.Unmarshal(list, &lst)
	path := "/api/passkeys/" + jsonNum(lst.Passkeys[0].ID)
	if c, _ := e.call(t, "DELETE", path, testOrigin, jsonBody(map[string]string{"code": "111111"})); c != http.StatusUnauthorized {
		t.Fatalf("delete with a wrong code: %d", c)
	}
	if c, b := e.call(t, "DELETE", path, testOrigin, jsonBody(map[string]string{"code": e.freshCode(t)})); c != http.StatusOK {
		t.Fatalf("delete: %d %s", c, b)
	}
	if e.passkeyOffered(t) {
		t.Fatal("passkey still offered after removal")
	}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

// WebAuthn needs HTTPS on a domain: a bare IP or plain http is refused
// before any code is checked, with a reason the settings page explains.
func TestPasskeyNeedsDomainAndHTTPS(t *testing.T) {
	cases := map[string]string{
		"http://192.168.1.10:8080": "passkey_ip",
		"https://10.0.0.5":         "passkey_ip",
		"http://[fd00::1]:8080":    "passkey_ip",
		"http://mon.lan":           "passkey_insecure",
	}
	for origin, want := range cases {
		e := newEnv(t, func(o *Options) { o.Config.PublicOrigin = origin })
		e.login(t)
		c, b := e.call(t, "POST", "/api/passkeys/begin", origin, jsonBody(map[string]string{"code": e.freshCode(t)}))
		if c != http.StatusBadRequest || !strings.Contains(string(b), want) {
			t.Errorf("%s: %d %s, want %s", origin, c, b, want)
		}
		if c, b := e.call(t, "POST", "/api/auth/passkey/begin", origin, nil); c != http.StatusBadRequest || !strings.Contains(string(b), want) {
			t.Errorf("%s sign-in: %d %s, want %s", origin, c, b, want)
		}
	}
	for origin, want := range map[string]string{
		"http://localhost:8080":        "",
		"http://srvmon.localhost":      "",
		"https://mon.example.com":      "",
		"https://mon.example.com:8443": "",
	} {
		_, _, got := passkeyOrigin(&http.Request{Header: http.Header{"Origin": {origin}}})
		if got != want {
			t.Errorf("%s: %q, want %q", origin, got, want)
		}
	}
}

// A passkey belongs to the domain it was made on: opened at another
// address, the panel does not offer it.
func TestPasskeyBoundToDomain(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Config.PublicOrigin = testOrigin })
	_, _ = e.st.AddPasskey(context.Background(), store.Passkey{CredID: "x", RPID: "old.example.test", Name: "old", Data: "{}"})
	if e.passkeyOffered(t) {
		t.Fatal("a passkey of another domain is offered")
	}
	if c, _ := e.call(t, "POST", "/api/auth/passkey/begin", testOrigin, nil); c != http.StatusNotFound {
		t.Fatalf("sign-in begin with a passkey of another domain: %d", c)
	}
	// srvmon-server init (new credentials) drops passkeys.
	if err := e.st.SetCredentials(context.Background(), "h", "s", nil); err != nil {
		t.Fatal(err)
	}
	if keys, _ := e.st.ListPasskeys(context.Background()); len(keys) != 0 {
		t.Fatalf("passkeys kept after init: %d", len(keys))
	}
}

// Two sign-ins answered with the same counter at once: only one passes, so
// the stored counter cannot be rolled back by a race.
func TestPasskeyConcurrentSignIns(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Config.PublicOrigin = testOrigin })
	e.login(t)
	key := newSoftKey(t, testOrigin, "mon.example.test")
	_, opts := e.call(t, "POST", "/api/passkeys/begin", testOrigin, jsonBody(map[string]string{"code": e.freshCode(t)}))
	if c, b := e.call(t, "POST", "/api/passkeys/finish", testOrigin, key.create(t, opts)); c != http.StatusOK {
		t.Fatalf("add: %d %s", c, b)
	}
	key.counter = 7
	var answers [2][]byte
	var clients [2]*env
	for i := range clients {
		c := *e
		jar, _ := cookiejar.New(nil)
		c.client = &http.Client{Jar: jar}
		clients[i] = &c
		_, o := c.call(t, "POST", "/api/auth/passkey/begin", testOrigin, nil)
		answers[i] = key.get(t, o)
	}
	codes := make(chan int, 2)
	for i := range clients {
		go func(i int) {
			c, _ := clients[i].call(t, "POST", "/api/auth/passkey/finish", testOrigin, answers[i])
			codes <- c
		}(i)
	}
	ok := 0
	for range clients {
		if <-codes == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d sign-ins passed with the same counter, want 1", ok)
	}
}
