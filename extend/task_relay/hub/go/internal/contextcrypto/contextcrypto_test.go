package contextcrypto_test

import (
	"encoding/json"
	"testing"

	"github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/contextcrypto"
)

func TestShouldEncryptInlineOnly(t *testing.T) {
	if !contextcrypto.ShouldEncrypt(`{"inline":"hello"}`) {
		t.Fatal("expected inline to require encryption")
	}
	if contextcrypto.ShouldEncrypt(`{"ref":{"uri":"https://x"}}`) {
		t.Fatal("expected ref to skip encryption")
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	original := `{"inline":"secret prompt"}`
	stored, err := contextcrypto.EncryptContextJSON(original, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if stored == original {
		t.Fatal("expected encrypted envelope")
	}
	restored, err := contextcrypto.DecryptContextJSON(stored, "secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(restored)
	if string(raw) != original {
		t.Fatalf("roundtrip mismatch: %s", string(raw))
	}
}
