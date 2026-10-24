package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

const KeyEnv = "SQLWISE_CONNECTION_KEY"

var errSealed = errors.New("stored credential cannot be decrypted")

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(encoded string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New(KeyEnv + " must be 32 random bytes encoded as base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plaintext string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (c *Cipher) Open(sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return "", errSealed
	}
	plaintext, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errSealed
	}
	return string(plaintext), nil
}
