package zsxq

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

const knowledgePlanetPublicKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEArbJvWdwi4w96rNjQQQTs
qzMefjZVP5CrZ+5vNj/qG5zefzqZa9o87pAWzH3MG/HW+0k9DzHv33cxIk4yQcy6
NJb/QuDCLYUoCjkoefa6rienCTruyYNFhFt/JCCTNd2UecS914cbr+5YKp81mPGj
QuVBwu8akI7NVZLKe+vufhn0/sNeWzmn4v/kKWwsrWy1q+8LfKGidFiNMJtRDTHG
kRDOKW8M8sIgNowp1ot/m00QB65j1B/rqAsTLod0bSe0W++v5SEkNh+XrEO9/d+c
zsDOeUL+NqhO6+EPjwJVxHn0PnvgaNKQ51OpniNV9WLtEyjv/A674zot2zA9VEX/
MwIDAQAB
-----END PUBLIC KEY-----`

type loginCipher struct {
	key       []byte
	iv        []byte
	encrypted string
}

func newLoginCipher() (*loginCipher, error) {
	key, err := randomBase64Text(16)
	if err != nil {
		return nil, err
	}
	iv, err := randomBase64Text(16)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(knowledgePlanetPublicKey))
	if block == nil {
		return nil, errors.New("invalid Knowledge Planet public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("Knowledge Planet public key is not RSA")
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, key)
	if err != nil {
		return nil, err
	}
	return &loginCipher{key: key, iv: iv, encrypted: base64.StdEncoding.EncodeToString(encrypted)}, nil
}

func randomBase64Text(length int) ([]byte, error) {
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	return []byte(encoded[:length]), nil
}

func (c *loginCipher) headers() (encryptedKey, encodedIV string) {
	return c.encrypted, base64.StdEncoding.EncodeToString(c.iv)
}

func (c *loginCipher) encrypt(plain []byte) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, c.iv).CryptBlocks(encrypted, padded)
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func (c *loginCipher) decrypt(encoded string) ([]byte, error) {
	encrypted, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, ErrSchemaDrift
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, c.iv).CryptBlocks(plain, encrypted)
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return nil, ErrSchemaDrift
	}
	return plain, nil
}

func pkcs7Pad(input []byte, blockSize int) []byte {
	padding := blockSize - len(input)%blockSize
	return append(input, bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func pkcs7Unpad(input []byte, blockSize int) ([]byte, error) {
	if len(input) == 0 || len(input)%blockSize != 0 {
		return nil, errors.New("invalid PKCS#7 data")
	}
	padding := int(input[len(input)-1])
	if padding == 0 || padding > blockSize || padding > len(input) {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	for _, value := range input[len(input)-padding:] {
		if int(value) != padding {
			return nil, errors.New("invalid PKCS#7 padding")
		}
	}
	return input[:len(input)-padding], nil
}
