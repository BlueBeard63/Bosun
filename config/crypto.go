package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// SecretBox seals and opens values with AES-256-GCM, nonce-prefixed.
// Use it to keep config values encrypted at rest in a database:
//
//	box, _ := config.NewSecretBox(key32)              // key from KMS/env
//	config.Add(config.KVSource{Store: table, Decrypt: box.Open})
type SecretBox struct {
	aead cipher.AEAD
}

// NewSecretBox creates a SecretBox from a 16-, 24-, or 32-byte key
// (AES-128/192/256 respectively).
func NewSecretBox(key []byte) (*SecretBox, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("config: bad key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext.
func (b *SecretBox) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts nonce||ciphertext produced by Seal.
func (b *SecretBox) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < b.aead.NonceSize() {
		return nil, errors.New("config: sealed value too short")
	}
	nonce, ct := sealed[:b.aead.NonceSize()], sealed[b.aead.NonceSize():]
	return b.aead.Open(nil, nonce, ct, nil)
}
