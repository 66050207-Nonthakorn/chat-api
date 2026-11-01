package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"

	"cloudchat/internal/config"
)

type keyRing struct {
	currentID string
	keys      map[string][]byte
}

func newKeyRing(cfg config.Config) (keyRing, error) {
	if cfg.SessionEncryptionCurrentKeyID == "" {
		return keyRing{}, errors.New("session encryption current key ID is required")
	}
	if len(cfg.SessionEncryptionKeys) == 0 {
		return keyRing{}, errors.New("session encryption keys are required")
	}
	if _, ok := cfg.SessionEncryptionKeys[cfg.SessionEncryptionCurrentKeyID]; !ok {
		return keyRing{}, errors.New("session encryption current key ID is not in the key ring")
	}
	for _, key := range cfg.SessionEncryptionKeys {
		if len(key) != 32 {
			return keyRing{}, errors.New("session encryption keys must be 32 bytes")
		}
	}
	return keyRing{currentID: cfg.SessionEncryptionCurrentKeyID, keys: cfg.SessionEncryptionKeys}, nil
}

func (ring keyRing) seal(plaintext []byte) (keyID string, ciphertext []byte, err error) {
	key := ring.keys[ring.currentID]
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", nil, err
	}
	return ring.currentID, append(nonce, gcm.Seal(nil, nonce, plaintext, nil)...), nil
}

func (ring keyRing) open(keyID string, ciphertext []byte) ([]byte, error) {
	key, ok := ring.keys[keyID]
	if !ok {
		return nil, errors.New("session encryption key ID is unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("encrypted value is truncated")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	return gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
}
