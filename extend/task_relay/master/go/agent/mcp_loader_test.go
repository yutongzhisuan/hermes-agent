package agent

import (
	"context"
	"strings"
	"testing"
)

func TestMCPInstructionsEmptyServers(t *testing.T) {
	toolkit, err := LoadMCPTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("LoadMCPTools: %v", err)
	}
	if toolkit == nil {
		t.Fatal("expected non-nil toolkit")
	}
	if len(toolkit.Instructions) != 0 {
		t.Fatalf("expected no instructions, got %v", toolkit.Instructions)
	}
}

func TestMergeMCPInstructionsEmpty(t *testing.T) {
	base := "base instruction"
	if got := mergeMCPInstructions(base, nil); got != base {
		t.Fatalf("nil map changed base:\n%q", got)
	}
	if got := mergeMCPInstructions(base, map[string]string{}); got != base {
		t.Fatalf("empty map changed base:\n%q", got)
	}
}

func TestMergeMCPInstructionsSortedDeterministic(t *testing.T) {
	base := "base instruction"
	instructions := map[string]string{
		"zeta":  "zeta rules",
		"alpha": "alpha rules",
	}
	got := mergeMCPInstructions(base, instructions)

	if !strings.HasPrefix(got, base) {
		t.Fatalf("result must start with base instruction:\n%q", got)
	}
	alphaIdx := strings.Index(got, "### alpha")
	zetaIdx := strings.Index(got, "### zeta")
	if alphaIdx < 0 || zetaIdx < 0 {
		t.Fatalf("missing server headers:\n%q", got)
	}
	if alphaIdx > zetaIdx {
		t.Fatalf("servers not sorted by name:\n%q", got)
	}
	if !strings.Contains(got, "## MCP Server Instructions") {
		t.Fatalf("missing section header:\n%q", got)
	}
	if !strings.Contains(got, "alpha rules") || !strings.Contains(got, "zeta rules") {
		t.Fatalf("missing server instruction bodies:\n%q", got)
	}

	for i := 0; i < 20; i++ {
		if again := mergeMCPInstructions(base, instructions); again != got {
			t.Fatalf("non-deterministic output:\n%q\nvs\n%q", again, got)
		}
	}
}

func TestMergeMCPInstructionsSkipsEmpty(t *testing.T) {
	got := mergeMCPInstructions("base", map[string]string{"empty": ""})
	if got != "base" {
		t.Fatalf("empty instructions should not alter base:\n%q", got)
	}
}
