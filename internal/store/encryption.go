package store

import (
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
)

// Encryptor encrypts and decrypts token strings using NaCl secretbox.
type Encryptor struct {
	key      *[32]byte
	previous *[32]byte
}

// NewEncryptor returns an Encryptor keyed with key; previous, if set, is tried on Open during key rotation.
func NewEncryptor(key [32]byte, previous *[32]byte) *Encryptor {
	e := &Encryptor{key: &key}
	if previous != nil {
		prev := *previous
		e.previous = &prev
	}
	return e
}

// Seal encrypts plaintext and prepends a random nonce.
func (e *Encryptor) Seal(plaintext string) ([]byte, error) {
	if e.key == nil {
		return nil, fmt.Errorf("encryption key not configured")
	}
	var nonce [24]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return secretbox.Seal(nonce[:], []byte(plaintext), &nonce, e.key), nil
}

// Open decrypts a ciphertext produced by Seal.
func (e *Encryptor) Open(encrypted []byte) (string, error) {
	if e.key == nil {
		return "", fmt.Errorf("encryption key not configured")
	}
	if len(encrypted) < 24 {
		return "", fmt.Errorf("ciphertext too short")
	}
	var nonce [24]byte
	copy(nonce[:], encrypted[:24])
	plaintext, ok := secretbox.Open(nil, encrypted[24:], &nonce, e.key)
	if !ok && e.previous != nil {
		plaintext, ok = secretbox.Open(nil, encrypted[24:], &nonce, e.previous)
	}
	if !ok {
		return "", fmt.Errorf("decryption failed")
	}
	return string(plaintext), nil
}
