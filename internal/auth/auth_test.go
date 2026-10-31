package auth

import (
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword("correct horse", h); err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := VerifyPassword("x", "garbage"); err == nil {
		t.Fatal("expected error for malformed hash")
	}
}

// RFC 6238 appendix B test vector for SHA1 (8 digits there; we check the last 6).
func TestTOTPVector(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1234567890:  "005924",
		20000000000: "353130",
	}
	for ts, want := range cases {
		got, err := TOTPCode(secret, ts/30)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("t=%d: got %s want %s", ts, got, want)
		}
	}
}

func TestVerifyTOTPReplayAndSkew(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	step := TOTPStep(now)
	prev, _ := TOTPCode(secret, step-1)
	if s, ok := VerifyTOTP(secret, prev, now, 0); !ok || s != step-1 {
		t.Fatal("previous step should be accepted")
	}
	if _, ok := VerifyTOTP(secret, prev, now, step-1); ok {
		t.Fatal("replayed code accepted")
	}
	old, _ := TOTPCode(secret, step-3)
	if _, ok := VerifyTOTP(secret, old, now, 0); ok {
		t.Fatal("code outside skew accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if b, _ := l.Blocked("ip"); b {
			t.Fatalf("blocked too early at %d", i)
		}
		l.Fail("ip")
	}
	if b, _ := l.Blocked("ip"); !b {
		t.Fatal("expected block")
	}
	now = now.Add(61 * time.Second)
	if b, _ := l.Blocked("ip"); b {
		t.Fatal("block should expire")
	}
}

func TestTokenHashNormalizes(t *testing.T) {
	if HashToken(" ABcd ") != HashToken("abcd") {
		t.Fatal("hash should ignore case and spaces")
	}
}

func TestLimiterReserveRelease(t *testing.T) {
	l := NewLimiter(2, time.Minute)
	if ok, _ := l.Reserve("ip"); !ok {
		t.Fatal("first reserve")
	}
	l.Release("ip") // success gives the slot back
	for i := 0; i < 2; i++ {
		if ok, _ := l.Reserve("ip"); !ok {
			t.Fatalf("reserve %d", i)
		}
	}
	if ok, _ := l.Reserve("ip"); ok {
		t.Fatal("third reserve must be refused")
	}
}
