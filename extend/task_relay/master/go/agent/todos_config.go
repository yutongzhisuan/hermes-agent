package agent

import (
	"os"
	"path/filepath"
	"strings"
)

type TodosFileConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Path    string `json:"path" yaml:"path"`
}

type TodosConfig struct {
	Enabled bool
	Path    string
}

func (c TodosConfig) WithDefaults(session string) TodosConfig {
	if c.Path == "" {
		home, _ := os.UserHomeDir()
		c.Path = filepath.Join(home, ".task-relay", "todos-"+session+".json")
	}
	c.Path = expandTildePath(c.Path)
	return c
}

func expandTildePath(p string) string {
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func todosConfigFromFile(f *TodosFileConfig) *TodosConfig {
	if f == nil {
		return nil
	}
	return &TodosConfig{Enabled: f.Enabled, Path: f.Path}
}
