package client_test

import (
	"testing"

	"github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
)

func TestSignContextRefMatchesPythonCanonical(t *testing.T) {
	ref := client.ContextRef{
		URI:             "https://example.com/context.txt",
		SHA256:          "abc123",
		ContentEncoding: "gzip",
	}
	signature, err := client.SignContextRef(ref, "secret")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signature == "" {
		t.Fatal("expected non-empty signature")
	}
	if err := ref.Sign("secret"); err != nil {
		t.Fatalf("ref.Sign: %v", err)
	}
	if ref.Signature != signature {
		t.Fatalf("Sign mismatch: %q vs %q", ref.Signature, signature)
	}
	protoRef := ref.ToProtoContextRef()
	if protoRef.GetSignature() != signature {
		t.Fatalf("proto signature mismatch")
	}
}
