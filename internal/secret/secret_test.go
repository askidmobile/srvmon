package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k", "master.key")
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode: %v %v", st, err)
	}
	ct, err := b.Seal([]byte("private"), []byte("server:1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("private")) {
		t.Fatal("plaintext leaked")
	}
	// Reload: same key from file.
	b2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b2.Open(ct, []byte("server:1"))
	if err != nil || string(pt) != "private" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := b2.Open(ct, []byte("server:2")); err == nil {
		t.Fatal("aad must bind ciphertext to its row")
	}
	ct[len(ct)-1] ^= 1
	if _, err := b2.Open(ct, []byte("server:1")); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}
