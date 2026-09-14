// Package api serves the supervisor HTTP surface of design v3 §4.3, child 3:
// the supervisor API, the dashboard and the ACP bridge.
//
// §4.1 publishes it on 127.0.0.1:7777 only. §4.4 makes /healthz and /readyz
// the container's readiness contract: /healthz answers whenever the process is
// alive, /readyz only when every essential child is ready, so a container
// orchestrator sees the same picture DR-1 says a multi-process container
// otherwise hides.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/akynte/local-engineer/internal/procman"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
)

// Deps are the collaborators the API reports on. Every field is optional; a
// nil dependency is reported as "not configured" rather than crashing, because
// `le api` must come up far enough to explain a broken configuration.
type Deps struct {
	Procs   *procman.Manager
	Root    *store.Root
	Sandbox *sandbox.Report
	// Profile is the active hardware profile name (§9.2).
	Profile string
	// Ready is an extra readiness gate beyond the children, used for schema
	// migration and config validation.
	Ready func() error
}

// Server is the supervisor HTTP server.
type Server struct {
	deps    Deps
	log     *slog.Logger
	srv     *http.Server
	started time.Time
}

// New builds the server bound to addr.
func New(addr string, deps Deps, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{deps: deps, log: log, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /version", s.versionz)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/workspaces", s.workspaces)
	mux.HandleFunc("GET /v1/sandbox", s.sandboxReport)
	mux.HandleFunc("GET /v1/packets", s.packets)
	mux.HandleFunc("GET /", s.dashboard)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.withLogging(mux),
		// A local supervisor still gets timeouts: a stuck reverse proxy or a
		// half-open connection must not pin a goroutine forever.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// Addr reports the configured listen address.
func (s *Server) Addr() string { return s.srv.Addr }

// Serve listens and serves until the context is cancelled, then shuts down
// gracefully within grace.
func (s *Server) Serve(ctx context.Context, grace time.Duration) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("api: listen on %s: %w", s.srv.Addr, err)
	}
	s.log.Info("supervisor api listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Deliberately NOT derived from ctx: ctx is already cancelled, and
		// Shutdown needs a live context to drain in-flight requests within the
		// grace period. Deriving from ctx would abort every connection at once.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx) //nolint:contextcheck // see above
	}
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		// Health probes are frequent and uninteresting at info level.
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http",
			"method", r.Method, "path", r.URL.Path, "status", rec.code,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Health is the /healthz payload: the process is alive and this is what it is.
type Health struct {
	Status  string       `json:"status"`
	Version version.Info `json:"version"`
	Uptime  string       `json:"uptime"`
	PID     int          `json:"pid"`
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Health{
		Status:  "ok",
		Version: version.Current(),
		Uptime:  time.Since(s.started).Round(time.Second).String(),
		PID:     pid(),
	})
}

// Readiness is the /readyz payload.
type Readiness struct {
	Ready    bool             `json:"ready"`
	Reasons  []string         `json:"reasons,omitempty"`
	Children []procman.Status `json:"children,omitempty"`
	Profile  string           `json:"profile,omitempty"`
	Sandbox  []sandbox.Layer  `json:"sandbox_layers,omitempty"`
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	out := Readiness{Ready: true, Profile: s.deps.Profile}

	if s.deps.Ready != nil {
		if err := s.deps.Ready(); err != nil {
			out.Ready = false
			out.Reasons = append(out.Reasons, err.Error())
		}
	}
	if s.deps.Procs != nil {
		out.Children = s.deps.Procs.Status()
		if ok, reasons := s.deps.Procs.Ready(); !ok {
			out.Ready = false
			out.Reasons = append(out.Reasons, reasons...)
		}
	}
	if s.deps.Sandbox != nil {
		out.Sandbox = s.deps.Sandbox.Active
	}

	code := http.StatusOK
	if !out.Ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, out)
}

func (s *Server) versionz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, version.Current())
}

// Status is the /v1/status payload: everything an operator asks for first.
type Status struct {
	Version   version.Info     `json:"version"`
	Uptime    string           `json:"uptime"`
	Profile   string           `json:"profile,omitempty"`
	Children  []procman.Status `json:"children"`
	Sandbox   *sandbox.Report  `json:"sandbox,omitempty"`
	DataDir   string           `json:"data_dir,omitempty"`
	Goroutine int              `json:"goroutines"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := Status{
		Version: version.Current(), Uptime: time.Since(s.started).Round(time.Second).String(),
		Profile: s.deps.Profile, Sandbox: s.deps.Sandbox, Goroutine: runtime.NumGoroutine(),
	}
	if s.deps.Procs != nil {
		out.Children = s.deps.Procs.Status()
	}
	if s.deps.Root != nil {
		out.DataDir = s.deps.Root.Layout().Root()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	if s.deps.Root == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no data directory is open"})
		return
	}
	records, err := s.deps.Root.ListWorkspaces()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if records == nil {
		records = []store.Record{}
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) sandboxReport(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sandbox == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sandbox not initialised"})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Sandbox)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The dashboard is served from the same origin; nothing here should be
	// embeddable or sniffable.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

// PacketSample is one step's packet, for the §8.3 chart.
type PacketSample struct {
	At     int64  `json:"at"`
	TaskID string `json:"task_id,omitempty"`
	Tokens int    `json:"tokens"`
	// Budget is the cap that applied, so a bar can be read against its limit
	// rather than against the largest bar on screen.
	Budget int `json:"budget"`
	// Dropped counts slices that did not fit, which §8.3 names as the signal
	// that the cap is too small for the task.
	Dropped int `json:"dropped"`
}

// PacketMetrics is what the dashboard charts.
type PacketMetrics struct {
	Samples []PacketSample `json:"samples"`
	// Misses is the §8.3 primary metric: a needed file absent from the packet,
	// discovered later by a failure. It sits beside packet size because the two
	// are read together — a cap that is too small shows up here first.
	Misses    int    `json:"retrieval_misses"`
	Packets   int    `json:"packets"`
	Workspace string `json:"workspace,omitempty"`
}

// packets serves §8.3's "packet size per step is charted in the dashboard".
//
// The chart exists to make one thing visible that a number cannot: whether
// packets are pressing against the cap. A mean packet size well under the cap
// and a handful of steps pinned to it are very different situations, and only
// the second says the cap is doing the deciding.
func (s *Server) packets(w http.ResponseWriter, r *http.Request) {
	if s.deps.Root == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "no data directory is open"})
		return
	}
	records, err := s.deps.Root.ListWorkspaces()
	if err != nil || len(records) == 0 {
		// No workspace is not an error: a supervisor started outside one has
		// nothing to chart, and an empty chart says that honestly.
		writeJSON(w, http.StatusOK, PacketMetrics{})
		return
	}

	// The most recently opened workspace is the one being worked in.
	latest := records[0]
	for _, rec := range records {
		if rec.LastOpened.After(latest.LastOpened) {
			latest = rec
		}
	}
	st, err := s.deps.Root.OpenWorkspace(r.Context(), latest.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, PacketMetrics{})
		return
	}

	out := PacketMetrics{Workspace: latest.Name}
	rows, err := st.Telemetry().SQL().QueryContext(r.Context(), `
		SELECT COALESCE(ts,0), COALESCE(task_id,''), COALESCE(count,0), COALESCE(attrs,'{}')
		FROM events WHERE kind = 'packet_built' ORDER BY ts DESC LIMIT 200`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var sample PacketSample
			var attrs string
			if err := rows.Scan(&sample.At, &sample.TaskID, &sample.Tokens, &attrs); err != nil {
				break
			}
			var parsed struct {
				Budget  int `json:"budget"`
				Dropped int `json:"dropped"`
			}
			_ = json.Unmarshal([]byte(attrs), &parsed)
			sample.Budget, sample.Dropped = parsed.Budget, parsed.Dropped
			out.Samples = append(out.Samples, sample)
		}
	}
	out.Packets = len(out.Samples)

	if err := st.Telemetry().SQL().QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM events WHERE kind = 'retrieval_miss'`).Scan(&out.Misses); err != nil {
		out.Misses = 0
	}
	writeJSON(w, http.StatusOK, out)
}
