package policy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// PathRules gates filesystem access by glob patterns.
// Patterns match against both the root-relative and absolute forms of the path.
type PathRules struct {
	AllowList []string `json:"allow_list" yaml:"allow_list"`
	DenyList  []string `json:"deny_list" yaml:"deny_list"`
}

// PathEvaluator decides whether a filesystem path may be accessed.
type PathEvaluator interface {
	EvaluatePath(path string) Decision
	Root() string
}

type pathEvaluator struct {
	root  string
	rules PathRules
}

// NewPathEvaluator resolves root to an absolute, symlink-free path.
func NewPathEvaluator(root string, rules PathRules) (PathEvaluator, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return &pathEvaluator{root: abs, rules: rules}, nil
}

func (e *pathEvaluator) Root() string { return e.root }

func (e *pathEvaluator) EvaluatePath(path string) Decision {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(e.root, abs)
	}
	abs = filepath.Clean(abs)
	raw := abs
	abs = resolveSymlinks(abs)

	rel, relErr := filepath.Rel(e.root, abs)
	insideRoot := relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))

	for _, d := range e.rules.DenyList {
		if matchAny(d, rel, abs, raw) {
			return Deny
		}
	}

	if !insideRoot {
		// Outside root: only an absolute allow glob may grant access.
		for _, a := range e.rules.AllowList {
			if filepath.IsAbs(a) && matchAny(a, rel, abs, raw) {
				return Allow
			}
		}
		return Deny
	}

	if len(e.rules.AllowList) == 0 {
		return Allow
	}
	for _, a := range e.rules.AllowList {
		if !filepath.IsAbs(a) && matchAny(a, rel, abs, raw) {
			return Allow
		}
	}
	return Deny
}

// resolveSymlinks resolves as much of the path as exists; missing tails are kept as-is
// so write targets for not-yet-created files still evaluate correctly.
func resolveSymlinks(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	dir := filepath.Dir(p)
	base := filepath.Base(p)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, base)
	}
	return p
}

// matchAny tries the pattern against the root-relative form, the symlink-resolved
// absolute form, and the raw absolute form (rules may carry unresolved prefixes).
func matchAny(pattern string, forms ...string) bool {
	for _, f := range forms {
		if ok, _ := doublestar.Match(pattern, f); ok {
			return true
		}
	}
	return false
}
