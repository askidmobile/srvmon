package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default, required by Google Authenticator
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters compatible with Google Authenticator.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // accept ±1 step
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random base32 secret (160 bits).
func NewTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// TOTPURI returns the otpauth:// URI to encode into a QR code.
func TOTPURI(secret, issuer, account string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", fmt.Sprint(totpDigits))
	v.Set("period", fmt.Sprint(totpPeriod))
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + v.Encode()
}

// TOTPCode computes the code for the given 30-second step.
func TOTPCode(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("auth: bad totp secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%1_000_000), nil
}

// TOTPStep returns the time step for t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// VerifyTOTP checks code against secret at time now. lastStep is the last
// step that was accepted; codes for that step or earlier are rejected to
// prevent replay. On success it returns the matched step.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, step)
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// NewRecoveryCodes returns n human-friendly one-time codes like "a1b2-c3d4-e5f6".
func NewRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, n)
	for i := range codes {
		buf := make([]byte, 6)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		h := hex.EncodeToString(buf)
		codes[i] = h[0:4] + "-" + h[4:8] + "-" + h[8:12]
	}
	return codes, nil
}

// HashToken returns a hex sha256 of a high-entropy token (session ids,
// agent tokens, recovery codes). These are random, so a fast hash is fine.
func HashToken(token string) string {
	s := sha256.Sum256([]byte(normalizeToken(token)))
	return hex.EncodeToString(s[:])
}

func normalizeToken(t string) string {
	return strings.ToLower(strings.TrimSpace(t))
}

// NewToken returns a random URL-safe token with the given prefix.
func NewToken(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + strings.ToLower(b32.EncodeToString(buf)), nil
}
