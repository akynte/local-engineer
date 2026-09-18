package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
)

// AgentName is the restricted agent a confined session runs under.
const AgentName = "le-editor"

// Session describes one confined OpenCode session.
//
// The architecture review is explicit that the shell is not part of the trust
// boundary: OpenCode's permission and plugin-hook enforcement has documented
// bypasses (subagents skipping tool.execute.before, deny lists ignored by the
// SDK), so a design that relied on them would be relying on a bug report. The
// boundary is the OS sandbox around the process and the supervisor's own checks
// behind the MCP tools. What this type adds is the part that was missing: the
// process was started by the developer, outside any sandbox, which left nothing
// to enforce. A session started here is confined before it can run a tool.
//
// The agent definition RegisterAgent writes is the convenience layer on top —
// it keeps an honest model away from tools it should not reach, and it is not
// what stops a dishonest one.
type Session struct {
	// Binary is the resolved OpenCode executable.
	Binary string
	// Repo is the worktree root: the only writable repository path.
	Repo string
	// StateDir is this workspace's private XDG home, so one workspace's
	// session never reads another's history, credentials or caches (§2.2).
	StateDir string
	// TmpDir is the per-workspace tmp the sandbox exposes as the only tmp.
	TmpDir string
}

// Confine derives the session's sandbox spec from the supervisor's base spec.
//
// The base carries the operator's read-only toolchain paths and the TCP grants
// for the inference endpoint; this adds what the session itself needs and
// nothing else.
func (s Session) Confine(base sandbox.Spec) (sandbox.Spec, error) {
	if s.Binary == "" || s.Repo == "" || s.StateDir == "" || s.TmpDir == "" {
		return sandbox.Spec{}, fmt.Errorf("opencode: a confined session needs a binary, a repository, a state directory and a tmp directory")
	}
	spec := base
	spec.Dir = s.Repo
	spec.TmpDir = s.TmpDir
	// The one child that legitimately needs egress: the session talks to the
	// model gateway, and a gateway it cannot reach is not a confined session
	// but a broken one. Everything else a task runs keeps §9's default of no
	// network, which is where that default does its work.
	spec.Network = sandbox.NetworkHost
	// Device nodes every ordinary program expects, granted read-write because
	// /dev/null is written to constantly. Without them an interpreted runtime
	// does not report a denied open — it faults, which reads as the
	// interpreter being broken rather than as a missing grant.
	write := append([]string{s.Repo, s.StateDir, s.TmpDir}, base.ReadWrite...)
	spec.ReadWrite = dedupe(append(write, recipe.DeviceFiles()...))

	// The interpreter's own tree has to be readable or the exec fails with the
	// 126 that recipe.Runner explains at length. OpenCode ships as a launcher
	// in bin/ beside the node_modules it loads, so the install root is granted
	// rather than the one file.
	read := append([]string(nil), base.ReadOnly...)
	read = append(read, installRoots(s.Binary)...)
	read = append(read, runtimeReadPaths...)
	if self, err := os.Executable(); err == nil {
		// `le mcp` is started by the session over stdio: the supervisor's tools
		// are reachable from inside the sandbox, and are the only route to a
		// verification command or a write the plan did not declare.
		read = append(read, installRoots(self)...)
	}
	spec.ReadOnly = dedupe(read)
	spec.Env = s.Env()
	return spec, spec.Validate()
}

// Env builds the child environment from nothing rather than filtering the
// parent's.
//
// A denylist is the wrong shape here: it has to be right about every variable
// anyone might export, and the ones that matter — an SSH agent socket, a cloud
// session token, an API key a developer sourced into their shell an hour ago —
// are exactly the ones a path-based sandbox cannot see. An allowlist is wrong
// only about variables the session then does without, which is a bug report
// rather than a disclosure.
func (s Session) Env() []string {
	env := []string{
		"HOME=" + s.StateDir,
		"TMPDIR=" + s.TmpDir,
		// OpenCode resolves its config, sessions, logs and credential store
		// through these. Pointing them at the workspace's own directory is
		// what keeps two workspaces' sessions apart (§2.2, §13).
		"XDG_CONFIG_HOME=" + filepath.Join(s.StateDir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(s.StateDir, "data"),
		"XDG_STATE_HOME=" + filepath.Join(s.StateDir, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(s.StateDir, "cache"),
	}
	// A terminal UI needs to know what it is drawing on, and a locale it does
	// not have renders as replacement characters. Neither carries a secret.
	for _, name := range []string{"PATH", "TERM", "COLORTERM", "LANG", "LC_ALL", "LC_CTYPE"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// runtimeReadPaths are what a managed runtime reads about itself and the
// machine before it runs any of the program's own code: its executable and
// memory map under /proc/self, the CPU and memory topology it sizes its heap
// and thread pool from, and the locale and certificate data any process reads.
//
// Granting /proc does not weaken a guarantee this system makes. §6.2 already
// states that seeing other tasks' processes is prevented only by the PID
// namespace, which the bubblewrap layer provides and the Landlock layer does
// not; that row is unchanged by this. Denying it does not produce a permission
// error either — an interpreted runtime that cannot read its own map exits
// silently, or faults, which is a worse failure than the one it prevents.
var runtimeReadPaths = []string{"/proc", "/sys", "/etc/localtime", "/etc/resolv.conf", "/etc/hosts"}

// installRoots returns the paths that must be readable for a binary to run: the
// resolved file's directory, and its parent when the file sits in a bin/, which
// is where an interpreted launcher keeps the tree it loads.
func installRoots(binary string) []string {
	resolved := binary
	if r, err := filepath.EvalSymlinks(binary); err == nil {
		resolved = r
	}
	dir := filepath.Dir(resolved)
	roots := []string{dir}
	if filepath.Base(dir) == "bin" {
		roots = append(roots, filepath.Dir(dir))
	}
	return roots
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// deniedPermissions are the tools a supervised edit session has no business
// reaching, with the reason each is refused.
//
// bash is the important one. §9 allows no free-form shell in the loop:
// verification is preset-only through le_verify, so that what ran is a command
// the operator froze during INTAKE and the result is tied to a content hash.
// A shell tool beside it is a second, unrecorded route to the same machine.
var deniedPermissions = map[string]string{
	"bash":               "verification runs through le_verify, which uses frozen presets; a shell beside it is an unrecorded route",
	"webfetch":           "repository text is data; fetched text is data from a party that chose it",
	"websearch":          "same, and an edit loop that searches the web is not reproducible",
	"task":               "one model, two roles: a subagent on one GPU costs a full prefill and answers with less context",
	"external_directory": "the worktree is the task's world; anything outside it is out of scope by construction",
	// The built-in file tools are denied so the proxied ones are the only
	// route. The sandbox already keeps the session inside the worktree; what
	// it cannot express is the rest of §9's path policy, and a committed .env
	// or a generated file is inside the worktree too.
	"read": "use le_read, which applies the supervisor's path policy and marks what it returns as repository data",
	"edit": "use le_edit, which applies the same policy and the write scope the task declared",
}

// RegisterAgent writes the restricted agent into the repository's opencode.json.
//
// It merges, like RegisterMCP, because the file is the developer's. The agent
// is additive: their own agents, model choice and permissions are untouched,
// and a session started outside `le opencode run` behaves exactly as before.
func RegisterAgent(repoRoot string) (path string, changed bool, err error) {
	return mergeConfig(repoRoot, func(doc map[string]any) bool {
		agents, _ := doc["agent"].(map[string]any)
		if agents == nil {
			agents = map[string]any{}
		}
		permission := map[string]any{}
		for tool := range deniedPermissions {
			permission[tool] = "deny"
		}
		// Finding files is not reading them: glob and list return paths, which
		// the sandbox already bounds, and grep is bounded the same way. Reading
		// and writing go through the proxied tools instead.
		for _, tool := range []string{"glob", "grep", "list"} {
			permission[tool] = "allow"
		}
		want := map[string]any{
			"description": "Supervised editing under Local Engineer. Verification, impact and " +
				"memory come from the le_* tools; this agent has no shell and no network.",
			"mode":       "primary",
			"permission": permission,
		}
		if equalJSON(agents[AgentName], want) {
			return false
		}
		agents[AgentName] = want
		doc["agent"] = agents
		return true
	})
}

// DeniedSummary renders the refusals for `le opencode run` to print, so the
// developer sees what the session cannot do before they start using it.
func DeniedSummary() string {
	tools := make([]string, 0, len(deniedPermissions))
	for tool := range deniedPermissions {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	var b strings.Builder
	for _, tool := range tools {
		fmt.Fprintf(&b, "  %-19s %s\n", tool, deniedPermissions[tool])
	}
	return b.String()
}
