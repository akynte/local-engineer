package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/opencode"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/supervisor"
)

func newOpenCodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "opencode",
		Short: "Wire this repository into OpenCode",
	}
	cmd.AddCommand(newOpenCodeSetupCmd())
	cmd.AddCommand(newOpenCodeRunCmd())
	return cmd
}

// newOpenCodeSetupCmd registers the MCP server and writes the project context
// OpenCode reads on its own.
//
// One command, run once, and afterwards a developer opens OpenCode in the
// directory and works normally. That is the whole point: a tool the user has to
// remember to invoke before asking a question is a tool they will stop using.
func newOpenCodeSetupCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register the MCP server and write AGENTS.md for this repository",
		Long: "setup makes Local Engineer part of an ordinary OpenCode session.\n\n" +
			"It registers `le mcp` in opencode.json, and writes a block into AGENTS.md —\n" +
			"which OpenCode reads into every session — telling the agent that a\n" +
			"compiler-backed index of this repository exists, which questions it answers\n" +
			"better than search, and what this repository has already recorded about\n" +
			"itself.\n\n" +
			"Re-run it after recording notes or re-indexing. It replaces only its own\n" +
			"block in AGENTS.md and merges into opencode.json, so anything you have\n" +
			"written in either file is left alone.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			out := cmd.OutOrStdout()

			command := []string{"le", "mcp"}
			if dataDir != "" {
				command = append(command, "--data", dataDir)
			}
			cfgPath, cfgChanged, err := opencode.RegisterMCP(ws.Root, command)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s\n", verb(cfgChanged), cfgPath)

			// The editor needs a model of its own, and the operator already
			// configured one for the supervisor. Copying it across is the
			// difference between "the tools are registered" and "you can type
			// a sentence and something happens".
			if cfg, err := loadConfig(root); err == nil {
				base, model := inferenceEndpoint(cfg, root)
				if _, modelChanged, err := opencode.RegisterModel(ws.Root, base, model); err != nil {
					return err
				} else if modelChanged {
					fmt.Fprintf(out, "wired the editor to %s (%s)\n", base, shortName(model))
				} else if base == "" {
					fmt.Fprintf(out, "no local endpoint configured yet — set inference.base_url "+
						"in le.yaml, or choose a model inside OpenCode\n")
				}
			}

			// The restricted agent, so `le opencode run` has one to select.
			if _, _, err := opencode.RegisterAgent(ws.Root); err != nil {
				return err
			}

			facts := opencode.Facts{WorkspaceName: ws.Name()}
			if stats, err := graph.New(st).Stats(ctx); err == nil {
				facts.Nodes, facts.Edges = stats.Nodes, stats.Edges
			}
			if notes, err := memory.Open(ws.Root, memory.DefaultCaps()).All(); err == nil {
				facts.Notes = notes
			}
			agentsPath, agentsChanged, err := opencode.Apply(ws.Root, opencode.Render(facts))
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s\n", verb(agentsChanged), agentsPath)

			if facts.Nodes == 0 {
				fmt.Fprintf(out, "\nThis repository is not indexed yet. Run `le index` once, "+
					"then `le opencode setup` again so AGENTS.md reports the real graph.\n")
			}
			fmt.Fprintf(out, "\nOpen this directory in OpenCode and work normally.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "",
		"pass an explicit --data to the registered `le mcp` command")
	return cmd
}

func verb(changed bool) string {
	if changed {
		return "wrote"
	}
	return "already current:"
}

// newOpenCodeRunCmd starts OpenCode inside the sandbox.
//
// `le opencode setup` made the supervisor's tools reachable from a session the
// developer starts themselves. That session is an ordinary process with the
// developer's whole environment: their home directory, their SSH agent, their
// cloud credentials, and a shell tool. The architecture review is direct about
// this — the coding shell's permission system is not part of the firewall,
// because its enforcement has documented bypasses — so the boundary has to be
// the OS sandbox around the process, which nothing was applying because nothing
// here started the process.
//
// This starts it: the strongest confinement the host permits, a scrubbed
// environment, this workspace's own XDG directories, and the restricted agent.
func newOpenCodeRunCmd() *cobra.Command {
	var unconfined bool
	cmd := &cobra.Command{
		Use:   "run [-- opencode args...]",
		Short: "Start OpenCode confined to this workspace",
		Long: "run starts an OpenCode session inside Local Engineer's sandbox.\n\n" +
			"The session can write its worktree and read the toolchain paths the\n" +
			"operator granted. It has no home directory, no inherited environment and\n" +
			"no network beyond the inference endpoint. Its shell, web and subagent\n" +
			"tools are refused, so verification goes through le_verify, where the\n" +
			"command is one the operator froze and the result is tied to a content\n" +
			"hash.\n\n" +
			"Arguments after -- are passed to OpenCode unchanged.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ws, root, st, err := openWorkspace(ctx)
			if err != nil {
				return err
			}
			defer closeRoot(cmd, root)
			out := cmd.OutOrStdout()

			binary, err := exec.LookPath("opencode")
			if err != nil {
				return fmt.Errorf("opencode is not on PATH: %w", err)
			}
			cfg, err := loadConfig(root)
			if err != nil {
				return err
			}
			dirs, err := st.TaskDirs()
			if err != nil {
				return err
			}

			// The agent is registered on every run rather than only by setup:
			// a developer who edits opencode.json between sessions should not
			// end up with a session whose restrictions silently went missing.
			if _, _, err := opencode.RegisterAgent(ws.Root); err != nil {
				return err
			}
			session := opencode.Session{
				Binary: binary, Repo: ws.Root,
				StateDir: st.OpenCodeDir(), TmpDir: dirs.Tmp,
			}
			spec, err := session.Confine(supervisor.BaseSandboxSpec(cfg, dirs))
			if err != nil {
				return err
			}
			if err := st.EnsureSandboxDirs(spec.ReadWrite); err != nil {
				return err
			}

			runner, report := supervisor.SelectSandbox(ctx, cfg)
			if runner == nil || (len(report.Active) == 1 && report.Active[0] == sandbox.LayerContainer && !inContainer()) {
				// Saying "confined" when nothing is confining is the failure
				// this refuses to make. The escape hatch is explicit and named.
				if !unconfined {
					return fmt.Errorf(
						"no sandbox layer is available on this host, so the session would run "+
							"unconfined with your whole environment:\n%s\n"+
							"Run `le doctor` to see why, or pass --unconfined to accept it",
						inactiveReasons(report))
				}
				fmt.Fprintf(out, "WARNING: starting unconfined. The shell's own permissions are not a boundary.\n\n")
			}

			argv := append([]string{binary, "--pure", "--agent", opencode.AgentName}, args...)
			child, err := runner.Command(ctx, spec, argv...)
			if err != nil {
				return err
			}
			child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), out, cmd.ErrOrStderr()

			fmt.Fprintf(out, "Starting OpenCode in %s under %s (%s).\n", ws.Name(), runner.Name(), layerList(report.Active))
			fmt.Fprintf(out, "Refused in this session:\n%s\n", opencode.DeniedSummary())

			if err := child.Run(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					// The session's own exit code is the developer's business,
					// not an error from this command.
					return nil
				}
				return fmt.Errorf("starting opencode: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&unconfined, "unconfined", false,
		"start even when no sandbox layer is available, accepting that the session is not confined")
	return cmd
}

func inContainer() bool { in, _ := sandbox.InContainer(); return in }

func layerList(layers []sandbox.Layer) string {
	names := make([]string, len(layers))
	for i, l := range layers {
		names[i] = string(l)
	}
	return strings.Join(names, " + ")
}

func inactiveReasons(report sandbox.Report) string {
	var b strings.Builder
	for _, note := range report.Inactive {
		fmt.Fprintf(&b, "  %s: %s\n", note.Layer, note.Reason)
	}
	if b.Len() == 0 {
		b.WriteString("  no layer reported a reason\n")
	}
	return b.String()
}

// inferenceEndpoint reports the base URL and model the supervisor is using, so
// the editor can be pointed at the same one.
//
// The provider file is the authority for the model name: it is what the
// supervisor sends, so it is what the endpoint will answer to.
func inferenceEndpoint(cfg config.Config, root *store.Root) (baseURL, model string) {
	switch cfg.Inference.Mode {
	case config.ModeExternal:
		baseURL = cfg.Inference.BaseURL
	case config.ModeEmbedded:
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Inference.Port)
	default:
		return "", ""
	}
	specs, err := llm.LoadProvidersFile(root.Layout().ConfigDir())
	if err != nil {
		return baseURL, ""
	}
	for _, spec := range specs.Providers {
		if spec.Name == specs.Default {
			return baseURL, spec.Model
		}
	}
	return baseURL, ""
}

func shortName(model string) string {
	return strings.TrimSuffix(filepath.Base(model), ".gguf")
}
