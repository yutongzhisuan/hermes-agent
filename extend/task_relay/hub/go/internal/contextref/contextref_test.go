package contextref_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	hubref "github.com/infa/xhermes-agent/extend/task_relay/hub/go/internal/contextref"
	master "github.com/infa/xhermes-agent/extend/task_relay/master/go/client"
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
