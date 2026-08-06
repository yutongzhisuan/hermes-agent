package contextcrypto_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/contextcrypto"
)

func TestShouldEncryptInlineOnly(t *testing.T) {
	require.True(t, contextcrypto.ShouldEncrypt(`{"inline":"hello"}`))
	require.False(t, contextcrypto.ShouldEncrypt(`{"ref":{"uri":"https://x"}}`))
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	original := `{"inline":"secret prompt"}`
	stored, err := contextcrypto.EncryptContextJSON(original, "secret")
	require.NoError(t, err)
	require.NotEqual(t, original, stored)

	restored, err := contextcrypto.DecryptContextJSON(stored, "secret")
	require.NoError(t, err)
	raw, err := json.Marshal(restored)
	require.NoError(t, err)
	require.JSONEq(t, original, string(raw))
}
