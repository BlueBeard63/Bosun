package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// sealV1 prefixes ciphertexts bound to their config key.
const sealV1 byte = 0x01

// SecretBox seals and opens config values with AES-GCM. Every value is bound
// to its config key: the key is passed to the AEAD as additional
// authenticated data, so a ciphertext copied under a different key fails to
// open. Use it to keep config values encrypted at rest in a database:
//
//	box, _ := config.NewSecretBox(key32)              // key from KMS/env
//	sealed, _ := box.Seal("ratelimit", plaintext)     // admin write path
//	config.Add(config.KVSource{Store: table, Decrypt: box.Open})
//
// Sealed values are 0x01 || nonce || ciphertext. Values sealed before key
// binding (nonce || ciphertext, no additional data) are rejected unless
// AllowLegacy is set; re-encrypt them with Reseal.
type SecretBox struct {
	// AllowLegacy makes Open also accept values sealed by earlier versions,
	// which aren't bound to a key. Enable it only while migrating stored
	// values with Reseal; set it before the box is first used.
	AllowLegacy bool

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

// Seal encrypts plaintext bound to the config key it will be stored under,
// returning 0x01 || nonce || ciphertext.
func (b *SecretBox) Seal(key string, plaintext []byte) ([]byte, error) {
	out := make([]byte, 1+b.aead.NonceSize(), 1+b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	out[0] = sealV1
	nonce := out[1:]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(out, nonce, plaintext, additionalData(key)), nil
}

// Open decrypts a value produced by Seal for the same config key. Its
// signature matches KVSource.Decrypt.
func (b *SecretBox) Open(key string, sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) >= 1+ns && sealed[0] == sealV1 {
		plain, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], additionalData(key))
		if err == nil || !b.AllowLegacy {
			return plain, err
		}
		// a legacy nonce can start with 0x01 too; fall through
	}
	if !b.AllowLegacy {
		if len(sealed) < 1+ns {
			return nil, errors.New("config: sealed value too short")
		}
		return nil, errors.New("config: sealed value is in the legacy unbound format; re-encrypt it with SecretBox.Reseal")
	}
	return b.openLegacy(sealed)
}

// Reseal re-encrypts a value sealed by an earlier version (not bound to a
// key) so that it is bound to key. Run it once over stored values when
// upgrading; values already in the current format are returned unchanged
// if they open under key.
func (b *SecretBox) Reseal(key string, sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) >= 1+ns && sealed[0] == sealV1 {
		if _, err := b.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], additionalData(key)); err == nil {
			return sealed, nil
		}
	}
	plain, err := b.openLegacy(sealed)
	if err != nil {
		return nil, err
	}
	return b.Seal(key, plain)
}

// openLegacy opens nonce || ciphertext sealed without additional data.
func (b *SecretBox) openLegacy(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("config: sealed value too short")
	}
	return b.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
}

// additionalData binds a ciphertext to its format version and config key.
func additionalData(key string) []byte {
	return append([]byte{sealV1}, key...)
}
