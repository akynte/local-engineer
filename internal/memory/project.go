package memory

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/worktree"
	"gopkg.in/yaml.v3"
)

type ProjectHeader struct {
	RepoID    string    `yaml:"repo_id" json:"repo_id"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at"`
	Commit    string    `yaml:"commit" json:"commit"`
	Source    string    `yaml:"source" json:"source"`
	Symbols   []string  `yaml:"symbols,omitempty" json:"symbols,omitempty"`
}
type ProjectNote struct {
	ProjectHeader
	File   string `json:"file"`
	Text   string `json:"text"`
	Stale  bool   `json:"stale"`
	Reason string `json:"reason,omitempty"`
}

// LoadProject loads only the named, human-editable project cards. Symbol facts
// are checked through the live index and stale cards remain visibly stale.
func LoadProject(root, repoID string, resolve func(string) (bool, error)) ([]ProjectNote, error) {
	var out []ProjectNote
	for _, name := range []string{"project", "architecture", "conventions", "constraints", "decisions", "glossary"} {
		file := ".agent/" + name + ".md"
		full, err := worktree.Resolve(root, file)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 16000 {
			return nil, fmt.Errorf("memory card %s exceeds 16KB", file)
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(string(body), "---\n", 3)
		if len(parts) != 3 || parts[0] != "" {
			return nil, fmt.Errorf("memory card %s requires YAML front matter", file)
		}
		var note ProjectNote
		if err := yaml.NewDecoder(bytes.NewBufferString(parts[1])).Decode(&note.ProjectHeader); err != nil {
			return nil, err
		}
		note.File, note.Text = file, strings.TrimSpace(parts[2])
		if note.RepoID != repoID || note.Commit == "" || note.UpdatedAt.IsZero() {
			note.Stale = true
			note.Reason = "missing or mismatched provenance"
		}
		if note.Source != "user" && note.Source != "model_accepted" && note.Source != "tool" {
			note.Stale = true
			note.Reason = "unreviewed source"
		}
		for _, symbol := range note.Symbols {
			found, err := resolve(symbol)
			if err != nil {
				return nil, err
			}
			if !found {
				note.Stale = true
				note.Reason = "unresolved symbol: " + symbol
			}
		}
		out = append(out, note)
	}
	return out, nil
}

var safeTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func WriteTaskCard(root, id string, header ProjectHeader, body string) (string, error) {
	if !safeTaskID.MatchString(id) {
		return "", fmt.Errorf("invalid task memory identifier")
	}
	file := ".agent/tasks/" + id + ".md"
	front, err := yaml.Marshal(header)
	if err != nil {
		return "", err
	}
	text := append([]byte("---\n"), front...)
	text = append(text, []byte("---\n\n"+body+"\n")...)
	return file, worktree.WriteWithin(root, file, text)
}
