package contextref

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Error indicates ContextRef signature is missing or invalid.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

// Ref carries the signed ContextRef fields.
type Ref struct {
	URI             string
	SHA256          string
	ContentEncoding string
	Signature       string
}

func canonical(ref Ref) string {
	return strings.Join([]string{ref.URI, ref.SHA256, ref.ContentEncoding}, "\n")
}

// Sign returns a hex HMAC-SHA256 signature for a ContextRef.
func Sign(ref Ref, secret string) (string, error) {
	if secret == "" {
		return "", &Error{Msg: "signing secret is required"}
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical(ref)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Verify raises Error when the ref signature is missing or invalid.
func Verify(ref Ref, secret string) error {
	if ref.Signature == "" {
		return &Error{Msg: "ContextRef.signature is required"}
	}
	expected, err := Sign(Ref{
		URI:             ref.URI,
		SHA256:          ref.SHA256,
		ContentEncoding: ref.ContentEncoding,
	}, secret)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(ref.Signature), []byte(expected)) {
		return &Error{Msg: "ContextRef.signature is invalid"}
	}
	return nil
}

// RefFromMap builds a Ref from a decoded context ref dict.
func RefFromMap(raw map[string]any) (Ref, error) {
	if raw == nil {
		return Ref{}, fmt.Errorf("ref is nil")
	}
	ref := Ref{
		URI:             stringField(raw, "uri"),
		SHA256:          stringField(raw, "sha256"),
		ContentEncoding: stringField(raw, "content_encoding"),
		Signature:       stringField(raw, "signature"),
	}
	return ref, nil
}

func stringField(raw map[string]any, key string) string {
	if v, ok := raw[key].(string); ok {
		return v
	}
	return ""
}
