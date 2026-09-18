package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// ServerProcess is operator configuration, never model-supplied input. Each
// managed profile uses the same gateway; switching unloads the previous owned
// process before loading the next. An existing external server is never killed.
type ServerProcess struct {
	Argv                []string `yaml:"argv"`
	BinarySHA256        string   `yaml:"binary_sha256"`
	ReadyTimeoutSeconds int      `yaml:"ready_timeout_seconds,omitempty"`
}
type managedProvider struct {
	Provider
	process ServerProcess
	address string
	key     string
}
type processSlot struct {
	gate  chan struct{}
	owner *managedProvider
	cmd   *exec.Cmd
	done  chan error
}

var localSlot = processSlot{gate: make(chan struct{}, 1)}
var externalSlots sync.Map

func newManaged(p Provider, spec ProviderSpec) (Provider, error) {
	u, err := url.Parse(spec.BaseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return nil, fmt.Errorf("managed model endpoint must use http://127.0.0.1:port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("managed model endpoint needs an explicit port")
	}
	config := *spec.Process
	if len(config.Argv) == 0 || !filepath.IsAbs(config.Argv[0]) || len(config.BinarySHA256) != 64 {
		return nil, fmt.Errorf("managed profile requires an absolute executable and binary_sha256 pin")
	}
	if config.ReadyTimeoutSeconds == 0 {
		config.ReadyTimeoutSeconds = 300
	}
	if config.ReadyTimeoutSeconds < 1 || config.ReadyTimeoutSeconds > 1800 {
		return nil, fmt.Errorf("managed startup timeout must be 1–1800 seconds")
	}
	// Final flags enforce one slot and a local endpoint even if a profile
	// accidentally contains earlier conflicting values.
	config.Argv = append(append([]string(nil), config.Argv...), "--parallel", "1", "--host", "127.0.0.1", "--port", u.Port())
	key, _ := json.Marshal(config)
	return &managedProvider{Provider: p, process: config, address: u.Host, key: string(key)}, nil
}

func (s *processSlot) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *processSlot) release() { <-s.gate }
func (s *processSlot) stop() error {
	if s.cmd == nil {
		return nil
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("model process did not exit; refusing to load another model")
		}
	}
	s.cmd = nil
	s.owner = nil
	s.done = nil
	return nil
}
func (p *managedProvider) prepare(ctx context.Context) error {
	if localSlot.owner != nil && localSlot.owner.key == p.key {
		select {
		case <-localSlot.done:
			localSlot.cmd = nil
			localSlot.owner = nil
		default:
			return nil
		}
	}
	if err := localSlot.stop(); err != nil {
		return err
	}
	file, err := os.Open(p.process.Argv[0])
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	file.Close()
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != p.process.BinarySHA256 {
		return fmt.Errorf("model server executable differs from its pinned hash")
	}
	// The probe is only asking whether the port is free, so it takes the
	// caller's context and gives it back immediately.
	var probe net.ListenConfig
	listener, err := probe.Listen(ctx, "tcp", p.address)
	if err != nil {
		return fmt.Errorf("model endpoint already occupied; refusing to replace an external server: %w", err)
	}
	_ = listener.Close()
	// Not CommandContext: the server outlives prepare by design, and the slot
	// stops it. Binding it here would kill it when prepare returned.
	//nolint:gosec,noctx // argv is operator configuration pinned by the SHA-256 check above, and the server must outlive this call
	cmd := exec.Command(p.process.Argv[0], p.process.Argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	localSlot.cmd, localSlot.owner, localSlot.done = cmd, p, make(chan error, 1)
	done := localSlot.done
	go func() { done <- cmd.Wait() }()
	ready, cancel := context.WithTimeout(ctx, time.Duration(p.process.ReadyTimeoutSeconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			localSlot.cmd = nil
			localSlot.owner = nil
			return fmt.Errorf("model server exited during startup: %w", err)
		case <-ready.Done():
			_ = localSlot.stop()
			return fmt.Errorf("model startup: %w", ready.Err())
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ready, time.Second)
			err := p.Provider.Health(probe)
			cancel()
			if err == nil {
				return nil
			}
		}
	}
}
func (p *managedProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if err := localSlot.acquire(ctx); err != nil {
		return nil, err
	}
	defer localSlot.release()
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	return p.Provider.Chat(ctx, req)
}
func (p *managedProvider) ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	if err := localSlot.acquire(ctx); err != nil {
		return nil, err
	}
	defer localSlot.release()
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	return p.Provider.ChatStructured(ctx, req, schema)
}
func (p *managedProvider) Health(ctx context.Context) error {
	if err := localSlot.acquire(ctx); err != nil {
		return err
	}
	defer localSlot.release()
	if err := p.prepare(ctx); err != nil {
		return err
	}
	return p.Provider.Health(ctx)
}

func (p *managedProvider) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if err := localSlot.acquire(ctx); err != nil {
		return nil, err
	}
	defer localSlot.release()
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	return p.Provider.Embed(ctx, req)
}
func (p *managedProvider) Infill(ctx context.Context, req InfillRequest) (*ChatResponse, error) {
	if err := localSlot.acquire(ctx); err != nil {
		return nil, err
	}
	defer localSlot.release()
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	return p.Provider.Infill(ctx, req)
}
func (p *managedProvider) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := localSlot.acquire(ctx); err != nil {
		return err
	}
	defer localSlot.release()
	if localSlot.owner == p {
		if err := localSlot.stop(); err != nil {
			return err
		}
	}
	return p.Provider.Close()
}

// serializedProvider prevents concurrent requests to the same externally owned
// local endpoint. Managed daily/deep profiles additionally share process state.
type serializedProvider struct {
	Provider
	gate chan struct{}
}

func serializeLocal(p Provider, endpoint string) Provider {
	gate, _ := externalSlots.LoadOrStore(endpoint, make(chan struct{}, 1))
	ch, ok := gate.(chan struct{})
	if !ok {
		// Only this function ever stores into the map, so this cannot happen;
		// serializing on a fresh channel is still the safe reading of it.
		ch = make(chan struct{}, 1)
	}
	return &serializedProvider{Provider: p, gate: ch}
}
func (p *serializedProvider) lock(ctx context.Context) error {
	select {
	case p.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *serializedProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if err := p.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-p.gate }()
	return p.Provider.Chat(ctx, req)
}
func (p *serializedProvider) ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	if err := p.lock(ctx); err != nil {
		return nil, err
	}
	defer func() { <-p.gate }()
	return p.Provider.ChatStructured(ctx, req, schema)
}
