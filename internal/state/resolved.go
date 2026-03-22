// Package state manages persistent state stores for the AgentClaw pipeline.
//
// The ResolvedStore tracks error patterns that agents have encountered and the
// resolutions that were found. This lets the system avoid repeating known
// mistakes by injecting resolution context into agent system prompts.
//
// Directory layout (relative to stateBaseDir):
//
//	resolved/
//	  index.json          — searchable ErrorPattern index (written atomically)
//	  <id>.md             — full resolution detail per unique error pattern
//
// Constraints:
//   - No external dependencies: only encoding/json, crypto/sha256, os, sync.
//   - index.json writes are atomic: write to index.tmp.json, then os.Rename.
//   - A single sync.Mutex guards all access (index.json is one file).
//   - Detail files are immutable after creation; updates only touch index.json.
package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrorPattern is one entry in the searchable error pattern index.
type ErrorPattern struct {
	ID                string    `json:"id"`                 // first 6 hex chars of SHA256(error_pattern)
	ErrorPattern      string    `json:"error_pattern"`      // normalized error string
	Tags              []string  `json:"tags"`               // e.g. ["minimax","timeout","api"]
	AgentRoles        []string  `json:"agent_roles"`        // which roles encountered this
	ResolutionSummary string    `json:"resolution_summary"` // one-line fix
	Severity          string    `json:"severity"`           // low | medium | high | critical
	OccurrenceCount   int       `json:"occurrence_count"`
	FirstSeen         time.Time `json:"first_seen"`
	LastSeen          time.Time `json:"last_seen"`
	DetailFile        string    `json:"detail_file"` // filename in resolved/
	Resolved          bool      `json:"resolved"`
}

// ResolvedStore is the file-backed error pattern store.
// All exported methods are safe for concurrent use.
type ResolvedStore struct {
	dir   string // absolute path to state/resolved/
	mu    sync.Mutex
	cache []ErrorPattern // in-memory cache; nil means not yet loaded
}

// NewResolvedStore creates a ResolvedStore rooted at stateBaseDir/resolved/.
// It creates the directory and an empty index.json if they do not exist.
func NewResolvedStore(stateBaseDir string) (*ResolvedStore, error) {
	dir := filepath.Join(stateBaseDir, "resolved")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("state: create resolved dir: %w", err)
	}

	r := &ResolvedStore{dir: dir}

	// Initialise an empty index if the file does not exist.
	indexPath := r.indexPath()
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		if werr := r.writeIndex(nil); werr != nil {
			return nil, fmt.Errorf("state: init index.json: %w", werr)
		}
	}

	return r, nil
}

// ─── Search ──────────────────────────────────────────────────────────────────

// Search returns up to 3 ErrorPattern entries that are most relevant to
// errorMsg and agentRole. Entries are scored and only those with score >= 3
// are returned, sorted by score descending.
func (r *ResolvedStore) Search(errorMsg string, agentRole string) ([]ErrorPattern, error) {
	r.mu.Lock()
	cached, err := r.loadIndex()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	// Copy the slice under the lock so concurrent Save() cannot mutate our backing array.
	patterns := make([]ErrorPattern, len(cached))
	copy(patterns, cached)
	r.mu.Unlock()

	normalized := normalizeMsg(errorMsg)

	type scored struct {
		pattern ErrorPattern
		score   int
	}
	var results []scored

	for _, p := range patterns {
		s := 0

		// +3 if error_pattern is substring of normalized message or vice versa
		normPat := normalizeMsg(p.ErrorPattern)
		if strings.Contains(normalized, normPat) || strings.Contains(normPat, normalized) {
			s += 3
		}

		// +2 for each matching tag found in normalized message
		for _, tag := range p.Tags {
			if strings.Contains(normalized, strings.ToLower(tag)) {
				s += 2
			}
		}

		// +1 if agentRole is in agent_roles
		for _, role := range p.AgentRoles {
			if role == agentRole {
				s++
				break
			}
		}

		// +1 per occurrence capped at 3
		occ := p.OccurrenceCount
		if occ > 3 {
			occ = 3
		}
		s += occ

		if s >= 3 {
			results = append(results, scored{pattern: p, score: s})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > 3 {
		results = results[:3]
	}

	out := make([]ErrorPattern, len(results))
	for i, r := range results {
		out[i] = r.pattern
	}
	return out, nil
}

// ─── Save ─────────────────────────────────────────────────────────────────────

// Save persists a new or updated error pattern.
// If the pattern's ID already exists, OccurrenceCount, LastSeen, AgentRoles,
// and ResolutionSummary (if non-empty) are updated. The detail file is always
// written (overwrites if exists). index.json is written atomically.
func (r *ResolvedStore) Save(pattern ErrorPattern, fullDetail string) error {
	// Generate deterministic ID from the error pattern string.
	h := sha256.Sum256([]byte(pattern.ErrorPattern))
	id := fmt.Sprintf("%x", h[:3]) // 6 hex chars

	r.mu.Lock()
	defer r.mu.Unlock()

	patterns, err := r.loadIndex()
	if err != nil {
		return err
	}

	now := time.Now()
	found := false
	for i, p := range patterns {
		if p.ID == id {
			patterns[i].OccurrenceCount++
			patterns[i].LastSeen = now
			// Deduplicate AgentRoles
			patterns[i].AgentRoles = mergeStrings(patterns[i].AgentRoles, pattern.AgentRoles)
			if pattern.ResolutionSummary != "" {
				patterns[i].ResolutionSummary = pattern.ResolutionSummary
			}
			found = true
			break
		}
	}

	detailFile := id + ".md"

	if !found {
		pattern.ID = id
		pattern.OccurrenceCount = 1
		pattern.FirstSeen = now
		pattern.LastSeen = now
		pattern.DetailFile = detailFile
		patterns = append(patterns, pattern)
	}

	// Write detail file (immutable after creation per spec — but we write
	// unconditionally here so that the first write always lands).
	detailPath := filepath.Join(r.dir, detailFile)
	if werr := os.WriteFile(detailPath, []byte(fullDetail), 0640); werr != nil {
		return fmt.Errorf("state: write detail file: %w", werr)
	}

	return r.writeIndex(patterns)
}

// ─── MarkResolved ─────────────────────────────────────────────────────────────

// MarkResolved sets Resolved=true for the entry with matching ID.
// Returns an error if the ID is not found.
func (r *ResolvedStore) MarkResolved(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	patterns, err := r.loadIndex()
	if err != nil {
		return err
	}

	for i, p := range patterns {
		if p.ID == id {
			patterns[i].Resolved = true
			return r.writeIndex(patterns)
		}
	}
	return fmt.Errorf("state: resolved: id %q not found", id)
}

// ─── LoadAll ─────────────────────────────────────────────────────────────────

// LoadAll returns all ErrorPattern entries sorted by OccurrenceCount descending.
func (r *ResolvedStore) LoadAll() ([]ErrorPattern, error) {
	r.mu.Lock()
	cached, err := r.loadIndex()
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// Copy before sorting to avoid mutating the shared cache slice in-place (#70).
	patterns := make([]ErrorPattern, len(cached))
	copy(patterns, cached)

	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].OccurrenceCount > patterns[j].OccurrenceCount
	})
	return patterns, nil
}

// LoadDetail returns the raw markdown content of the detail file for a pattern ID.
func (r *ResolvedStore) LoadDetail(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	detailPath := filepath.Join(r.dir, id+".md")
	data, err := os.ReadFile(detailPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("state: detail file for id %q not found", id)
		}
		return "", fmt.Errorf("state: read detail file: %w", err)
	}
	return string(data), nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (r *ResolvedStore) indexPath() string {
	return filepath.Join(r.dir, "index.json")
}

func (r *ResolvedStore) tmpPath() string {
	return filepath.Join(r.dir, "index.tmp.json")
}

// loadIndex returns the cached patterns slice, loading from disk on first access.
// Caller must hold r.mu.
func (r *ResolvedStore) loadIndex() ([]ErrorPattern, error) {
	if r.cache != nil {
		return r.cache, nil
	}
	data, err := os.ReadFile(r.indexPath())
	if err != nil {
		return nil, fmt.Errorf("state: read index.json: %w", err)
	}
	var patterns []ErrorPattern
	if err := json.Unmarshal(data, &patterns); err != nil {
		return nil, fmt.Errorf("state: parse index.json: %w", err)
	}
	r.cache = patterns
	return r.cache, nil
}

// writeIndex atomically writes patterns to index.json and updates the cache.
// Caller must hold r.mu.
func (r *ResolvedStore) writeIndex(patterns []ErrorPattern) error {
	if patterns == nil {
		patterns = []ErrorPattern{}
	}
	data, err := json.MarshalIndent(patterns, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal index: %w", err)
	}
	tmp := r.tmpPath()
	if err := os.WriteFile(tmp, data, 0640); err != nil {
		return fmt.Errorf("state: write index.tmp.json: %w", err)
	}
	if err := os.Rename(tmp, r.indexPath()); err != nil {
		return fmt.Errorf("state: rename index.tmp.json: %w", err)
	}
	// Update the in-memory cache only after the atomic rename succeeds.
	r.cache = patterns
	return nil
}

// normalizeMsg lowercases, trims, and collapses whitespace in s.
func normalizeMsg(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Collapse all whitespace sequences to a single space.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// mergeStrings returns a deduplicated union of a and b.
func mergeStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		seen[s] = true
	}
	out := append([]string(nil), a...)
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
