package client

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	pb "github.com/infa/hermes-agent/extend/task_relay/gen/go"
)

// ContextRef mirrors the Task Relay ContextRef message for signing helpers.
type ContextRef struct {
	URI             string
	SHA256          string
	ContentEncoding string
	Signature       string
}

func canonicalContextRef(ref ContextRef) string {
	return strings.Join([]string{
		ref.URI,
		ref.SHA256,
		ref.ContentEncoding,
	}, "\n")
}

// SignContextRef returns the hex HMAC-SHA256 signature for a ContextRef.
func SignContextRef(ref ContextRef, secret string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("signing secret is required")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte(canonicalContextRef(ref))); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Sign mutates ref.Signature using the provided secret.
func (ref *ContextRef) Sign(secret string) error {
	signature, err := SignContextRef(*ref, secret)
	if err != nil {
		return err
	}
	ref.Signature = signature
	return nil
}

// ToProtoContextRef converts a signed helper struct to protobuf.
func (ref ContextRef) ToProtoContextRef() *pb.ContextRef {
	return &pb.ContextRef{
		Uri:             ref.URI,
		Sha256:          ref.SHA256,
		ContentEncoding: ref.ContentEncoding,
		Signature:       ref.Signature,
	}
}
