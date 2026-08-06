package client_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/master/client"
)

func TestSignContextRefMatchesPythonCanonical(t *testing.T) {
	ref := client.ContextRef{
		URI:             "https://example.com/context.txt",
		SHA256:          "abc123",
		ContentEncoding: "gzip",
	}
	signature, err := client.SignContextRef(ref, "secret")
	require.NoError(t, err)
	require.NotEmpty(t, signature)

	require.NoError(t, ref.Sign("secret"))
	require.Equal(t, signature, ref.Signature)

	protoRef := ref.ToProtoContextRef()
	require.Equal(t, signature, protoRef.GetSignature())
}
