package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/memory"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Durable notes kept with the repository: intent, observation, advice",
		Long: "Memory is split into three kinds because they are not interchangeable.\n\n" +
			"  intent       why something is being done: a requirement, a constraint\n" +
			"  observation  something that was seen once; evidence, not a rule\n" +
			"  advice       a rule meant to steer future work\n\n" +
			"Every note records where it came from. A rule with no source cannot be\n" +
			"judged, and a system that writes rules about its own work will write\n" +
			"flattering ones. Notes live under .le/memory/ so they travel with the\n" +
			"repository, and there is no global store.",
	}
	cmd.AddCommand(newMemoryListCmd(), newMemoryAddCmd(), newMemoryRemoveCmd())
	return cmd
}

// memoryStore binds to the workspace the command was run in.
func memoryStore() (*memory.Store, string, error) {
	ws, err := openWorkspaceOnly()
	if err != nil {
		return nil, "", err
	}
	return memory.Open(ws.Root, memory.DefaultCaps()), ws.Name(), nil
}

func newMemoryListCmd() *cobra.Command {
	var kind string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show the notes kept with this repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, _, err := memoryStore()
			if err != nil {
				return err
			}
			all, err := s.All()
			if err != nil {
				return err
			}
			if kind != "" {
				k := memory.Kind(kind)
				if !k.Valid() {
					return fmt.Errorf("%q is not one of intent, observation, advice", kind)
				}
				all = map[memory.Kind][]memory.Note{k: all[k]}
			}
			if asJSON {
				return emitJSON(all)
			}

			out := cmd.OutOrStdout()
			var total int
			for _, k := range memory.Kinds() {
				notes := all[k]
				if len(notes) == 0 {
					continue
				}
				fmt.Fprintf(out, "%s (%d)\n", k, len(notes))
				for _, n := range notes {
					fmt.Fprintf(out, "  %-18s %s\n", n.ID, n.Text)
					fmt.Fprintf(out, "  %-18s from %s", "", n.Provenance.Source)
					if n.Provenance.Evidence != "" {
						fmt.Fprintf(out, ", evidence %s", n.Provenance.Evidence)
					}
					fmt.Fprintf(out, ", %s\n", n.Provenance.At.Format("2006-01-02"))
					total++
				}
				fmt.Fprintln(out)
			}
			if total == 0 {
				fmt.Fprintf(out, "No notes yet. `le memory add` records one.\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "show only intent, observation or advice")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return cmd
}

func newMemoryAddCmd() *cobra.Command {
	var kind, source, evidence string
	var tags []string
	cmd := &cobra.Command{
		Use:   "add <text>",
		Short: "Record a note",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := memoryStore()
			if err != nil {
				return err
			}
			k := memory.Kind(kind)
			if !k.Valid() {
				return fmt.Errorf("--kind must be intent, observation or advice, got %q", kind)
			}
			n, err := s.Add(memory.Note{
				Kind: k, Text: strings.Join(args, " "), Tags: tags,
				Provenance: memory.Provenance{Source: source, Evidence: evidence},
			})
			if err != nil {
				if errors.Is(err, memory.ErrNoProvenance) {
					return fmt.Errorf("%w\nPass --source to say where this came from: "+
						"\"operator\", a task id, or the tool that produced it", err)
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s\n", n.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "intent, observation or advice (required)")
	cmd.Flags().StringVar(&source, "source", "operator", "where this came from")
	cmd.Flags().StringVar(&evidence, "evidence", "", "the evidence id supporting it, if any")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "tags for filtering")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func newMemoryRemoveCmd() *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "remove <id>",
		Short: "Delete a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := memoryStore()
			if err != nil {
				return err
			}
			k := memory.Kind(kind)
			if !k.Valid() {
				return fmt.Errorf("--kind must be intent, observation or advice, got %q", kind)
			}
			ok, err := s.Remove(k, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no %s note with id %s", kind, args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "which kind the note is (required)")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

func newLessonsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lessons",
		Short: "Copy notes between repositories, explicitly",
		Long: "There is no global memory and no automatic channel between projects.\n" +
			"These two commands are the channel: export writes a file you can read,\n" +
			"import shows you what it would copy and asks before copying it.\n\n" +
			"Imported notes are marked as imported and keep their original source,\n" +
			"so a rule learned elsewhere never reads as one this repository\n" +
			"established. That is the difference between a lesson and a rumour.",
	}
	cmd.AddCommand(newLessonsExportCmd(), newLessonsImportCmd())
	return cmd
}

func newLessonsExportCmd() *cobra.Command {
	var out string
	var kinds []string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write this repository's notes to a file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, name, err := memoryStore()
			if err != nil {
				return err
			}
			selected, err := parseKinds(kinds)
			if err != nil {
				return err
			}
			b, err := s.Export(name, selected)
			if err != nil {
				return err
			}
			if len(b.Notes) == 0 {
				return fmt.Errorf("there are no notes of those kinds to export")
			}
			if out == "" {
				return memory.WriteBundle(cmd.OutOrStdout(), b)
			}
			if err := memory.ExportTo(out, b); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d note(s) to %s\n", len(b.Notes), out)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "to", "", "write to this path (default: stdout)")
	cmd.Flags().StringSliceVar(&kinds, "kind", []string{"advice"},
		"which kinds to export; intent is usually specific to one repository")
	return cmd
}

func newLessonsImportCmd() *cobra.Command {
	var kinds []string
	var yes bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Copy notes from a file into this repository, after showing them",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, _, err := memoryStore()
			if err != nil {
				return err
			}
			b, err := memory.ImportFrom(args[0])
			if err != nil {
				return err
			}
			selected, err := parseKinds(kinds)
			if err != nil {
				return err
			}

			// The design calls this "text you have read". Printing it is the
			// whole mechanism, so it happens before anything else.
			fmt.Fprint(cmd.OutOrStdout(), memory.Preview(b, selected))

			if !yes {
				fmt.Fprint(cmd.ErrOrStderr(), "\nImport these notes? [y/N] ")
				reply, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(reply)) != "y" {
					return fmt.Errorf("not imported")
				}
			}

			added, err := s.Import(b, memory.ImportOptions{Confirmed: true, Kinds: selected})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "imported %d note(s)\n", len(added))
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "restrict to these kinds")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func parseKinds(names []string) ([]memory.Kind, error) {
	var out []memory.Kind
	for _, n := range names {
		k := memory.Kind(strings.TrimSpace(n))
		if !k.Valid() {
			return nil, fmt.Errorf("%q is not one of intent, observation, advice", n)
		}
		out = append(out, k)
	}
	return out, nil
}

// openWorkspaceOnly resolves the workspace without opening its databases:
// memory lives in the repository, not in the data directory, so a note can be
// read or written without touching $LE_DATA at all.
func openWorkspaceOnly() (*workspace.Workspace, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ws, err := workspace.Open(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w\nRun `le workspace init` in the repository root first", err)
	}
	return ws, nil
}
