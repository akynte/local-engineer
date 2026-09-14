// Package version carries build identity stamped in at link time.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Values are overridden by -ldflags at release build time (see .goreleaser.yaml).
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// SchemaVersions records the current migration target for each database. The
// supervisor refuses to open a store whose on-disk version is newer than these
// (see DR-1: downgrades across schema versions are not supported).
var SchemaVersions = map[string]int{
	"index":     1,
	"ledger":    3,
	"telemetry": 1,
}

// Info is the payload reported by `le version` and /healthz.
type Info struct {
	Version           string         `json:"version"`
	Commit            string         `json:"commit"`
	Date              string         `json:"date"`
	Go                string         `json:"go"`
	Platform          string         `json:"platform"`
	Schemas           map[string]int `json:"schemas"`
	IndexerV          int            `json:"indexer_version"`
	WorkspaceIDScheme int            `json:"workspace_id_scheme"`
}

// IndexerVersion participates in index keys (§3.4): bumping it invalidates
// every cached analysis result.
const IndexerVersion = 1

// WorkspaceIDScheme is the version of the workspace identity derivation
// (§2.1). DR-6 requires this to be versioned so a re-key migration is possible.
const WorkspaceIDScheme = 1

func Current() Info {
	return Info{
		Version:           Version,
		Commit:            Commit,
		Date:              Date,
		Go:                runtime.Version(),
		Platform:          fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Schemas:           SchemaVersions,
		IndexerV:          IndexerVersion,
		WorkspaceIDScheme: WorkspaceIDScheme,
	}
}

// String is the one-line form used by `le version`.
func (i Info) String() string {
	return fmt.Sprintf("local-engineer %s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.Date, i.Go, i.Platform)
}

func init() {
	if Version != "0.0.0-dev" {
		return
	}
	// Best effort for `go install`ed builds that carry VCS stamps.
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				Commit = s.Value
			}
		case "vcs.time":
			if s.Value != "" {
				Date = s.Value
			}
		}
	}
}
