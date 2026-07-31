package auth_test

import (
	"testing"
	"time"

	"github.com/infa/hermes-agent/extend/task_relay/hub/go/internal/auth"
)

func TestIssueAndVerifyMasterJWT(t *testing.T) {
	verifier, err := auth.New("secret", "hermes-relay-hub", "task-relay-hub", time.Hour, nil)
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	token, err := verifier.IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	masterID, err := verifier.VerifyMasterJWT(token)
	if err != nil || masterID != "master-1" {
		t.Fatalf("verify: id=%q err=%v", masterID, err)
	}
}

func TestVerifyMasterJWTRejectsWorkerRole(t *testing.T) {
	verifier, err := auth.New("secret", "hermes-relay-hub", "task-relay-hub", time.Hour, nil)
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	token, err := verifier.IssueMasterJWT("master-1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := verifier.VerifyMasterJWT(token + "bad"); err == nil {
		t.Fatal("expected invalid token error")
	}
}
