package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/infa/task_relay/hub/internal/auth"
)

func TestIssueAndVerifyMasterJWT(t *testing.T) {
	verifier, err := auth.New("secret", "xhermes-relay-hub", "task-relay-hub", time.Hour, nil)
	require.NoError(t, err)

	token, err := verifier.IssueMasterJWT("master-1", time.Hour)
	require.NoError(t, err)

	masterID, err := verifier.VerifyMasterJWT(token)
	require.NoError(t, err)
	require.Equal(t, "master-1", masterID)
}

func TestVerifyMasterJWTRejectsWorkerRole(t *testing.T) {
	verifier, err := auth.New("secret", "xhermes-relay-hub", "task-relay-hub", time.Hour, nil)
	require.NoError(t, err)

	token, err := verifier.IssueMasterJWT("master-1", time.Hour)
	require.NoError(t, err)

	_, err = verifier.VerifyMasterJWT(token + "bad")
	require.Error(t, err)
}
