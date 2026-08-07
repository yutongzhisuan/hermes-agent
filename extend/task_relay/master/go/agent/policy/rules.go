package policy

import "strings"

type Mode string

const (
	ModeDenyByDefault     Mode = "deny_by_default"
	ModeAllowWithDenyList Mode = "allow_with_deny_list"
)

type Rules struct {
	Mode         Mode     `json:"mode" yaml:"mode"`
	AllowList    []string `json:"allow_list" yaml:"allow_list"`
	DenyList     []string `json:"deny_list" yaml:"deny_list"`
	ApprovalList []string `json:"approval_list" yaml:"approval_list"`
}

type ruleEvaluator struct {
	rules Rules
}

func NewEvaluator(r Rules) Evaluator {
	if r.Mode == "" {
		r.Mode = ModeDenyByDefault
	}
	return &ruleEvaluator{rules: r}
}

func commandHead(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (e *ruleEvaluator) Evaluate(command string) Decision {
	head := commandHead(command)
	if head == "" {
		return Deny
	}
	for _, d := range e.rules.DenyList {
		if strings.Contains(command, d) {
			return Deny
		}
	}
	for _, a := range e.rules.AllowList {
		if head == a || command == a || strings.HasPrefix(command, a+" ") {
			return Allow
		}
	}
	for _, a := range e.rules.ApprovalList {
		if head == a || command == a || strings.HasPrefix(command, a+" ") {
			return NeedsApproval
		}
	}
	if e.rules.Mode == ModeAllowWithDenyList {
		return Allow
	}
	return Deny
}
