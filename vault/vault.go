package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const formatVersion byte = 1

type Vault struct {
	aead cipher.AEAD
}

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256 key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return &Vault{aead: aead}, nil
}

func (v *Vault) Seal(plaintext, additionalData []byte) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	out := make([]byte, 1+2+len(nonce))
	out[0] = formatVersion
	binary.BigEndian.PutUint16(out[1:3], uint16(len(nonce)))
	copy(out[3:], nonce)
	out = v.aead.Seal(out, nonce, plaintext, additionalData)
	return out, nil
}

func (v *Vault) Open(ciphertext, additionalData []byte) ([]byte, error) {
	if len(ciphertext) < 3 || ciphertext[0] != formatVersion {
		return nil, errors.New("invalid encrypted value format")
	}
	n := int(binary.BigEndian.Uint16(ciphertext[1:3]))
	if n != v.aead.NonceSize() || len(ciphertext) < 3+n {
		return nil, errors.New("invalid encrypted value nonce")
	}
	plain, err := v.aead.Open(nil, ciphertext[3:3+n], ciphertext[3+n:], additionalData)
	if err != nil {
		return nil, errors.New("decrypting value: authentication failed")
	}
	return plain, nil
}
