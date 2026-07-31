package contextcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

const envelopeKey = "encrypted_inline"

// Error indicates inline context encryption or decryption failed.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

func deriveKey(secret string) ([]byte, error) {
	if secret == "" {
		return nil, &Error{Msg: "encryption secret is required"}
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:], nil
}

func encryptBytes(plaintext []byte, secret string) (string, error) {
	key, err := deriveKey(secret)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptBytes(blob, secret string) ([]byte, error) {
	key, err := deriveKey(secret)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, &Error{Msg: "invalid ciphertext encoding"}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, &Error{Msg: "ciphertext is too short"}
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, &Error{Msg: "decryption failed"}
	}
	return plaintext, nil
}

// ShouldEncrypt returns true when context JSON carries in-band inline payload.
func ShouldEncrypt(contextJSON string) bool {
	if contextJSON == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(contextJSON), &payload); err != nil {
		return false
	}
	_, hasInline := payload["inline"]
	_, hasGzip := payload["inline_gzip"]
	return hasInline || hasGzip
}

// EncryptContextJSON wraps inline context in an encrypted envelope for storage.
func EncryptContextJSON(contextJSON, secret string) (string, error) {
	if !ShouldEncrypt(contextJSON) {
		return contextJSON, nil
	}
	blob, err := encryptBytes([]byte(contextJSON), secret)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{
		envelopeKey: map[string]any{"v": 1, "data": blob},
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// DecryptContextJSON returns worker-ready context, decrypting stored envelopes.
func DecryptContextJSON(contextJSON, secret string) (any, error) {
	if contextJSON == "" {
		return nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(contextJSON), &payload); err != nil {
		return nil, err
	}
	envelope, ok := payload[envelopeKey].(map[string]any)
	if !ok {
		return payload, nil
	}
	blob, _ := envelope["data"].(string)
	if blob == "" {
		return nil, &Error{Msg: "encrypted_inline.data is required"}
	}
	plaintext, err := decryptBytes(blob, secret)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		return nil, fmt.Errorf("decode decrypted context: %w", err)
	}
	return decoded, nil
}
