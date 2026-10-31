// Package secret encrypts values at rest (SSH private keys) with
// XChaCha20-Poly1305 under a master key kept in a file outside the database.
package secret

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
)

// Box seals and opens values with the master key.
type Box struct {
	key []byte
}

// New builds a box from a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("secret: key must be %d bytes", chacha20poly1305.KeySize)
	}
	return &Box{key: append([]byte(nil), key...)}, nil
}

// LoadOrCreate reads the master key from path or creates it (mode 0600).
// Losing this file makes stored SSH keys unreadable, so it must be backed up
// together with the database but is never stored inside it.
func LoadOrCreate(path string) (*Box, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, chacha20poly1305.KeySize)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("secret: create master key: %w", err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("secret: read master key: %w", err)
	}
	return New(b)
}

// Seal encrypts plaintext; aad binds the ciphertext to its context (e.g. the
// server id) so it cannot be moved to another row.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open decrypts a value produced by Seal with the same aad.
func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("secret: ciphertext too short")
	}
	nonce, ct := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errors.New("secret: decrypt failed")
	}
	return pt, nil
}
