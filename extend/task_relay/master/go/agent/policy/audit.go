package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AuditEntry struct {
	JobID      string
	Command    string
	Backend    string
	Decision   string
	ExitCode   int
	DurationMs int64
	Stdout     string
	WorkDir    string
	Session    string
}

type auditRecord struct {
	TS         string `json:"ts"`
	JobID      string `json:"job_id"`
	Command    string `json:"command"`
	Backend    string `json:"backend"`
	Decision   string `json:"decision"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	StdoutHash string `json:"stdout_hash"`
	StdoutLen  int    `json:"stdout_len"`
	WorkDir    string `json:"workdir"`
	Session    string `json:"session"`
}

type AuditLogger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

func NewAuditLogger(path string) (*AuditLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit open: %w", err)
	}
	return &AuditLogger{file: f, enc: json.NewEncoder(f)}, nil
}

func (l *AuditLogger) Log(e AuditEntry) error {
	sum := sha256.Sum256([]byte(e.Stdout))
	rec := auditRecord{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		JobID:      e.JobID,
		Command:    e.Command,
		Backend:    e.Backend,
		Decision:   e.Decision,
		ExitCode:   e.ExitCode,
		DurationMs: e.DurationMs,
		StdoutHash: "sha256:" + hex.EncodeToString(sum[:]),
		StdoutLen:  len(e.Stdout),
		WorkDir:    e.WorkDir,
		Session:    e.Session,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(rec); err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return l.file.Sync()
}

func (l *AuditLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
