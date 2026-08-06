package contextref_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	hubref "github.com/infa/task_relay/hub/internal/contextref"
	master "github.com/infa/task_relay/master/client"
)

func TestSignContextRefMatchesMasterSDK(t *testing.T) {
	ref := hubref.Ref{
		URI: "https://example.com/context", SHA256: "abc123", ContentEncoding: "gzip",
	}
	hubSig, err := hubref.Sign(ref, "secret")
	require.NoError(t, err)

	masterSig, err := master.SignContextRef(master.ContextRef{
		URI: ref.URI, SHA256: ref.SHA256, ContentEncoding: ref.ContentEncoding,
	}, "secret")
	require.NoError(t, err)
	require.Equal(t, masterSig, hubSig)

	ref.Signature = hubSig
	require.NoError(t, hubref.Verify(ref, "secret"))
}
