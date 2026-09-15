package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/task"
)

// `le tui` — design v3 §4.1's "docker exec -it local-engineer le tui".
//
// Deliberately not a full-screen application. §4.1 asks for a way to watch the
// system from inside the container, and the thing an operator actually needs
// there is: are the children up, what is running, and is anything waiting for
// me. A cursor-addressed UI would add a dependency, break under `docker exec`
// without a TTY, and make the output impossible to pipe — and the web
// dashboard already exists for anyone who wants charts.
//
// So this is a repainting status view: it clears and redraws on an interval,
// degrades to plain appended output when stdout is not a terminal, and exits
// on Ctrl-C or after --once.
//
// What it will never do is act. Approving a gate is `le gate approve`, with the
// diff and the impact report in front of you (§3.3). A key that approved from a
// status screen would be a way to approve without reading, which is the failure
// the gates exist to prevent.

func newTUICmd() *cobra.Command {
	var interval time.Duration
	var once bool
	c := &cobra.Command{
		Use:   "tui",
		Short: "Watch the supervisor: children, tasks and waiting gates",
		Long: "A repainting status view for use inside the container (§4.1):\n\n" +
			"    docker exec -it local-engineer le tui\n\n" +
			"It shows what `le doctor` cannot — what is happening right now — and it is\n" +
			"read-only on purpose. Approving a gate is `le gate approve`, with the diff and\n" +
			"the impact report in front of you; a key that approved from a status screen\n" +
			"would be a way to approve without reading.\n\n" +
			"Without a terminal it appends plain blocks instead of repainting, so it can\n" +
			"be piped or redirected to a log.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			tty := isTerminal(os.Stdout)
			for {
				frame, err := renderTUI(ctx)
				if err != nil {
					return err
				}
				if tty && !once {
					// Clear and home. Two escapes rather than a library: this
					// is the entire terminal handling the view needs.
					fmt.Fprint(cmd.OutOrStdout(), "\033[2J\033[H")
				}
				fmt.Fprint(cmd.OutOrStdout(), frame)
				if once {
					return nil
				}
				if !tty {
					fmt.Fprintln(cmd.OutOrStdout())
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	c.Flags().DurationVar(&interval, "interval", 2*time.Second, "how often to refresh")
	c.Flags().BoolVar(&once, "once", false, "render a single frame and exit")
	return c
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// renderTUI builds one frame.
//
// Each section degrades independently: the supervisor may not be running, the
// working directory may not be a workspace, and neither should blank the rest
// of the view. A status screen that shows nothing because one thing is absent
// is worse than one that says which thing.
func renderTUI(ctx context.Context) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "local-engineer — %s\n\n", time.Now().Format("15:04:05"))

	renderSupervisor(ctx, &b)
	b.WriteString("\n")
	renderWorkspaceSection(ctx, &b)

	b.WriteString("\nread-only. `le gate show <id>` to read a gate, `le gate approve` to decide.\n")
	return b.String(), nil
}

func renderSupervisor(ctx context.Context, b *strings.Builder) {
	cfg := config.Default()
	if root, err := openRoot(); err == nil {
		if c, err := loadConfig(root); err == nil {
			cfg = c
		}
	}
	addr := cfg.API.Addr
	if addr == "" {
		addr = config.LoopbackAddr
	}
	base := "http://" + strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1)

	var st struct {
		Version struct {
			Version string `json:"version"`
		} `json:"version"`
		Uptime   string `json:"uptime"`
		Profile  string `json:"profile"`
		Children []struct {
			Name     string `json:"name"`
			State    string `json:"state"`
			Restarts int    `json:"restarts"`
		} `json:"children"`
		Sandbox *struct {
			Runner string   `json:"runner"`
			Active []string `json:"active_layers"`
		} `json:"sandbox"`
	}
	if err := getJSON(ctx, base+"/v1/status", &st); err != nil {
		fmt.Fprintf(b, "SUPERVISOR  not reachable at %s\n", base)
		fmt.Fprintf(b, "            (%v)\n", err)
		fmt.Fprintf(b, "            start it with `le api`, or run this inside the container\n")
		return
	}
	fmt.Fprintf(b, "SUPERVISOR  %s, up %s", st.Version.Version, st.Uptime)
	if st.Profile != "" {
		fmt.Fprintf(b, ", profile %s", st.Profile)
	}
	b.WriteString("\n")
	if st.Sandbox != nil {
		fmt.Fprintf(b, "SANDBOX     %s [%s]\n", st.Sandbox.Runner, strings.Join(st.Sandbox.Active, " "))
	}
	if len(st.Children) == 0 {
		fmt.Fprintln(b, "CHILDREN    none (inference is external or disabled)")
		return
	}
	for _, c := range st.Children {
		line := fmt.Sprintf("CHILDREN    %-16s %s", c.Name, c.State)
		if c.Restarts > 0 {
			line += fmt.Sprintf(" (%d restarts)", c.Restarts)
		}
		fmt.Fprintln(b, line)
	}
}

func renderWorkspaceSection(ctx context.Context, b *strings.Builder) {
	ws, root, st, err := openWorkspace(ctx)
	if err != nil {
		fmt.Fprintln(b, "WORKSPACE   not inside one (cd to a repository with .le/workspace.yaml)")
		return
	}
	fmt.Fprintf(b, "WORKSPACE   %s  %s\n", ws.Name(), ws.ID())

	renderTasks(ctx, st, b)
	renderGates(ctx, brokerFor(root, st), b)
}

func renderTasks(ctx context.Context, st *store.Store, b *strings.Builder) {
	tasks, err := task.NewStore(st).List(ctx)
	if err != nil {
		fmt.Fprintf(b, "TASKS       unreadable: %v\n", err)
		return
	}
	var live []task.Task
	for _, t := range tasks {
		if !t.State.Terminal() {
			live = append(live, t)
		}
	}
	if len(live) == 0 {
		fmt.Fprintf(b, "TASKS       none running (%d in total)\n", len(tasks))
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].UpdatedAt.After(live[j].UpdatedAt) })
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TASKS\tID\tSTATE\tLEVEL\tTITLE")
	for _, t := range live {
		title := t.Title
		if len(title) > 44 {
			title = title[:43] + "…"
		}
		fmt.Fprintf(w, "\t%s\t%s\t%s\t%s\n", t.ID, t.State, t.Verification, title)
	}
	_ = w.Flush()
}

func renderGates(ctx context.Context, bk *broker.Broker, b *strings.Builder) {
	gates, err := bk.Pending(ctx)
	if err != nil {
		fmt.Fprintf(b, "GATES       unreadable: %v\n", err)
		return
	}
	if len(gates) == 0 {
		fmt.Fprintln(b, "GATES       nothing waiting")
		return
	}
	// The one thing this view exists to make impossible to miss.
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "GATES\t%d WAITING FOR YOU\t\t\n", len(gates))
	for _, g := range gates {
		q := g.Question
		if len(q) > 52 {
			q = q[:51] + "…"
		}
		fmt.Fprintf(w, "\t%s\t%s\t%s\n", g.ID, g.Kind, q)
	}
	_ = w.Flush()
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
