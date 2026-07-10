package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ericlakich/squadron-dev/agent"
)

// Session records the state of one phase run, persisted as session.json inside
// the session directory so that workspace_status can report on it later.
type Session struct {
	ID           string                  `json:"id"`
	Phase        string                  `json:"phase"`
	Repo         string                  `json:"repo,omitempty"`
	Branch       string                  `json:"branch,omitempty"`
	BaseBranch   string                  `json:"base_branch,omitempty"`
	WorkspaceDir string                  `json:"workspace_dir"`
	Status       string                  `json:"status"`
	PRURL        string                  `json:"pr_url,omitempty"`
	Summary      string                  `json:"summary,omitempty"`
	Transcript   []agent.TranscriptEntry `json:"transcript,omitempty"`
	Error        string                  `json:"error,omitempty"`
	Iterations   int                     `json:"iterations,omitempty"`
	ToolCalls    int                     `json:"tool_calls,omitempty"`
	InputTokens  int                     `json:"input_tokens,omitempty"`
	OutputTokens int                     `json:"output_tokens,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

const sessionManifest = "session.json"

// newSession creates a session with a generated id and the directory layout
// under root: <root>/<id>/session.json (manifest) and <root>/<id>/repo (clone).
func newSession(root, phase string) *Session {
	id := fmt.Sprintf("%s-%s-%s", phase, time.Now().UTC().Format("20060102-150405"), shortID())
	now := time.Now().UTC()
	return &Session{
		ID:           id,
		Phase:        phase,
		WorkspaceDir: filepath.Join(root, id, "repo"),
		Status:       "running",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func sessionDir(root, id string) string { return filepath.Join(root, id) }

func (s *Session) save(root string) error {
	dir := sessionDir(root, s.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sessionManifest), b, 0o644)
}

func loadSession(root, id string) (*Session, error) {
	b, err := os.ReadFile(filepath.Join(sessionDir(root, id), sessionManifest))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q not found", id)
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func shortID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)
}
