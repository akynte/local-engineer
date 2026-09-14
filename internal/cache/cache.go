// Package cache implements the per-workspace cache of design v3 §2.2:
// "Keyed by workspace and content manifest; never shared."
//
// This package derives keys; internal/store owns the file I/O. That split is
// not stylistic: §2.3 forbids file writes outside internal/store and
// internal/artifacts, and the storescope analyzer fails the build otherwise.
//
// The key derivation mixes in the workspace id, so two workspaces holding
// byte-identical inputs still produce different keys and cannot hit each
// other's entries even if the directories were somehow merged.
package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync/atomic"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// ErrMiss is returned by Get when the entry is absent.
var ErrMiss = errors.New("cache: miss")

// Cache is a content-addressed cache bound to one workspace.
type Cache struct {
	ws    workspace.ID
	blobs *store.BlobDir

	hits, misses atomic.Int64
}

// New binds a cache to a workspace store. As everywhere else, the only way in
// is through a Store, which only store.OpenWorkspace can produce (§2.3).
func New(s *store.Store) *Cache {
	// analysis is the single namespace; per-analyzer separation is by key, not
	// by directory, so one Clear invalidates everything an indexer version
	// change should invalidate.
	blobs, err := s.Blobs("analysis")
	if err != nil {
		// Blobs only fails on a malformed namespace, which is a constant here.
		panic(fmt.Sprintf("cache: %v", err))
	}
	return &Cache{ws: s.ID(), blobs: blobs}
}

// WorkspaceID reports the owning workspace.
func (c *Cache) WorkspaceID() workspace.ID { return c.ws }

// Dir reports the backing directory, for diagnostics and tests.
func (c *Cache) Dir() string { return c.blobs.Dir() }

// Key derives the cache key for a namespace (package loads, TS programs, doc
// notes) and a content manifest. The workspace id and the indexer version are
// both part of the digest: an indexer upgrade invalidates every entry (§3.4),
// and a second workspace can never compute a key this one would hit (§2.3).
func (c *Cache) Key(namespace, manifest string) string {
	h := sha256.New()
	// Length-prefixed, not separator-delimited: see writeComponents in
	// internal/workspace for why a separator is not injective.
	writeComponents(h,
		fmt.Sprintf("le-cache-v%d", version.IndexerVersion),
		c.ws.String(), namespace, manifest)
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached bytes for a namespace and manifest, or ErrMiss.
func (c *Cache) Get(namespace, manifest string) ([]byte, error) {
	body, err := c.blobs.Get(c.Key(namespace, manifest))
	if err != nil {
		if errors.Is(err, store.ErrNoBlob) {
			c.misses.Add(1)
			return nil, ErrMiss
		}
		return nil, err
	}
	c.hits.Add(1)
	return body, nil
}

// Put stores bytes under a namespace and manifest.
func (c *Cache) Put(namespace, manifest string, body []byte) error {
	return c.blobs.Put(c.Key(namespace, manifest), body)
}

// Stats reports hit and miss counters. The isolation test asserts that a
// second workspace reading the same content produces misses only (§2.3).
type Stats struct {
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
}

func (c *Cache) Stats() Stats { return Stats{Hits: c.hits.Load(), Misses: c.misses.Load()} }

// Clear removes every entry for this workspace.
func (c *Cache) Clear() error { return c.blobs.Clear() }

// ManifestOf hashes a set of (path, content-hash) pairs into the content
// manifest that keys an entry. Callers pass the files an analysis depended on,
// so any change to any of them invalidates the entry (§3.4).
func ManifestOf(pairs map[string]string) string {
	h := sha256.New()
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeComponents(h, k, pairs[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeComponents feeds a length-prefixed encoding of each component into h,
// so that no two different component tuples can produce the same byte stream.
func writeComponents(h io.Writer, parts ...string) {
	var lenBuf [binary.MaxVarintLen64]byte
	for _, p := range parts {
		n := binary.PutUvarint(lenBuf[:], uint64(len(p)))
		h.Write(lenBuf[:n])
		io.WriteString(h, p)
	}
}
