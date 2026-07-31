package contextref_test

import (
	"testing"

	hubref "github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/contextref"
	master "github.com/infa/hermes-agent/extend/task_relay/master/go/client"
)

func TestSignContextRefMatchesMasterSDK(t *testing.T) {
	ref := hubref.Ref{
		URI: "https://example.com/context", SHA256: "abc123", ContentEncoding: "gzip",
	}
	hubSig, err := hubref.Sign(ref, "secret")
	if err != nil {
		t.Fatal(err)
	}
	masterSig, err := master.SignContextRef(master.ContextRef{
		URI: ref.URI, SHA256: ref.SHA256, ContentEncoding: ref.ContentEncoding,
	}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if hubSig != masterSig {
		t.Fatalf("signature mismatch hub=%s master=%s", hubSig, masterSig)
	}
	ref.Signature = hubSig
	if err := hubref.Verify(ref, "secret"); err != nil {
		t.Fatal(err)
	}
}
