package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// ErrNoSecretKey is returned when credentials are used without a key
// configured. Storing a password in the clear is refused rather than done
// quietly, because the operator would have no way to tell.
var ErrNoSecretKey = errors.New("no secret key configured; set ATSUME_SECRET_KEY to store credentials")

// Sealer encrypts stored secrets with AES-GCM.
//
// The key is derived from the configured passphrase rather than used directly,
// so any length of input works. GCM is authenticated, so a corrupted or
// tampered value fails to open instead of decrypting to rubbish.
type Sealer struct{ key []byte }

// NewSealer derives a key from a passphrase. An empty passphrase yields a
// sealer that refuses to work.
func NewSealer(passphrase string) *Sealer {
	if passphrase == "" {
		return &Sealer{}
	}
	sum := sha256.Sum256([]byte(passphrase))
	return &Sealer{key: sum[:]}
}

// Enabled reports whether secrets can be stored.
func (s *Sealer) Enabled() bool { return len(s.key) > 0 }

// Seal encrypts plaintext, returning nonce||ciphertext.
func (s *Sealer) Seal(plaintext string) ([]byte, error) {
	if !s.Enabled() {
		return nil, ErrNoSecretKey
	}
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed []byte) (string, error) {
	if !s.Enabled() {
		return "", ErrNoSecretKey
	}
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("sealed value is too short")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// Most often the key changed; say so rather than reporting a generic
		// authentication failure.
		return "", fmt.Errorf("could not decrypt; the secret key may have changed: %w", err)
	}
	return string(out), nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
