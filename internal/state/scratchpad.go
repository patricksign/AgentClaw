package state

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxScratchpadEntries is the number of entries kept in the live file before
// older entries are archived.
const maxScratchpadEntries = 50

// ScratchpadKind enumerates the valid entry kinds.
type ScratchpadKind string

const (
	KindInProgress ScratchpadKind = "in_progress"
	KindBlocked    ScratchpadKind = "blocked"
	KindDecision   ScratchpadKind = "decision"
	KindHandoff    ScratchpadKind = "handoff"
	KindWarning    ScratchpadKind = "warning"
)

// ScratchpadEntry is one entry written by an agent or operator.
type ScratchpadEntry struct {
	AgentID   string         `json:"agent_id"`
	Kind      ScratchpadKind `json:"kind"`
	Message   string         `json:"message"`
	TaskID    string         `json:"task_id"`
	Timestamp time.Time      `json:"timestamp"`
}

// Scratchpad manages the shared team scratchpad at state/scratchpad.md.
// All exported methods are safe for concurrent use.
type Scratchpad struct {
	mu      sync.Mutex
	path    string // e.g. ./state/scratchpad.md
	archDir string // e.g. ./state/old/
}

// NewScratchpad creates the scratchpad file and its archive directory if they
// do not already exist.
func NewScratchpad(stateBaseDir string) (*Scratchpad, error) {
	archDir := filepath.Join(stateBaseDir, "old")
	if err := os.MkdirAll(archDir, 0700); err != nil {
		return nil, fmt.Errorf("scratchpad: mkdir %s: %w", archDir, err)
	}
	path := filepath.Join(stateBaseDir, "scratchpad.md")
	s := &Scratchpad{path: path, archDir: archDir}
	// Create empty file if it does not exist.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := s.flush(nil); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// AddEntry appends a new entry. If the live file already holds
// maxScratchpadEntries, the oldest entries are archived before writing.
// Safe for concurrent use.
func (s *Scratchpad) AddEntry(entry ScratchpadEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readLocked()
	if err != nil {
		// Corrupted file — start fresh rather than blocking.
		entries = nil
	}

	entries = append(entries, entry)

	// Archive overflow.
	if len(entries) > maxScratchpadEntries {
		overflow := entries[:len(entries)-maxScratchpadEntries]
		entries = entries[len(entries)-maxScratchpadEntries:]
		if aerr := s.archive(overflow); aerr != nil {
			// E-2: log so operators are aware of dropped entries.
			slog.Warn("scratchpad: archive failed, entries dropped", "err", aerr)
		}
	}

	return s.flush(entries)
}

// Read parses all entries from the JSON block embedded in the file.
// Safe for concurrent use.
func (s *Scratchpad) Read() ([]ScratchpadEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

// readLocked is the internal read; caller must hold s.mu.
func (s *Scratchpad) readLocked() ([]ScratchpadEntry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scratchpad: read file: %w", err)
	}
	return extractJSON(string(data))
}

// ReadForContext returns a compact Markdown summary of entries from the last
// 24 h, capped at ~400 tokens (≈1600 chars).
func (s *Scratchpad) ReadForContext() (string, error) {
	entries, err := s.Read()
	if err != nil {
		return "", err
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	recent := make([]ScratchpadEntry, 0, len(entries))
	for _, e := range entries {
		if e.Timestamp.After(cutoff) {
			recent = append(recent, e)
		}
	}
	if len(recent) == 0 {
		return "", nil
	}

	groups := map[ScratchpadKind][]ScratchpadEntry{
		KindInProgress: {},
		KindBlocked:    {},
		KindDecision:   {},
		KindHandoff:    {},
		KindWarning:    {},
	}
	for _, e := range recent {
		groups[e.Kind] = append(groups[e.Kind], e)
	}

	order := []ScratchpadKind{KindInProgress, KindBlocked, KindDecision, KindHandoff, KindWarning}
	labels := map[ScratchpadKind]string{
		KindInProgress: "In Progress",
		KindBlocked:    "Blocked",
		KindDecision:   "Decisions",
		KindHandoff:    "Handoffs",
		KindWarning:    "Warnings",
	}

	var sb strings.Builder
	for _, kind := range order {
		list := groups[kind]
		if len(list) == 0 {
			continue
		}
		sb.WriteString("**")
		sb.WriteString(labels[kind])
		sb.WriteString("**\n")
		for _, e := range list {
			line := fmt.Sprintf("- [%s][%s] %s\n",
				e.Timestamp.Format("15:04"),
				e.AgentID,
				e.Message,
			)
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	out := sb.String()
	// Cap at ~1600 chars (~400 tokens).
	if len(out) > 1600 {
		out = out[:1600] + "\n…(truncated)"
	}
	return out, nil
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// flush serialises entries to the scratchpad file: a JSON block inside an HTML
// comment at the top (for machine parsing) followed by human-readable Markdown.
func (s *Scratchpad) flush(entries []ScratchpadEntry) error {
	jsonBytes, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("scratchpad: marshal: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("<!-- scratchpad-json\n")
	sb.Write(jsonBytes)
	sb.WriteString("\n-->\n\n")
	sb.WriteString("# Team Scratchpad\n\n")
	sb.WriteString("*Auto-generated. Edit via API or agent calls.*\n\n")

	if len(entries) == 0 {
		sb.WriteString("*No entries yet.*\n")
	} else {
		sb.WriteString(renderMarkdown(entries))
	}

	return os.WriteFile(s.path, []byte(sb.String()), 0600)
}

// archive writes overflow entries to state/old/scratchpad-<date>.md.
// Appends to the archive file if it already exists for today.
func (s *Scratchpad) archive(entries []ScratchpadEntry) error {
	if len(entries) == 0 {
		return nil
	}
	date := time.Now().Format("2006-01-02")
	archPath := filepath.Join(s.archDir, "scratchpad-"+date+".md")

	f, err := os.OpenFile(archPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("scratchpad: archive open: %w", err)
	}
	defer f.Close()

	jsonBytes, _ := json.MarshalIndent(entries, "", "  ")
	_, err = fmt.Fprintf(f, "\n<!-- archived: %s -->\n%s\n\n%s",
		time.Now().Format(time.RFC3339),
		string(jsonBytes),
		renderMarkdown(entries),
	)
	return err
}

// extractJSON parses the JSON block from the HTML comment at the top of the file.
func extractJSON(content string) ([]ScratchpadEntry, error) {
	const open = "<!-- scratchpad-json\n"
	const close = "\n-->"
	start := strings.Index(content, open)
	if start == -1 {
		return nil, nil
	}
	start += len(open)
	end := strings.Index(content[start:], close)
	if end == -1 {
		return nil, fmt.Errorf("scratchpad: malformed JSON block (no closing comment)")
	}
	raw := content[start : start+end]
	if strings.TrimSpace(raw) == "null" || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var entries []ScratchpadEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("scratchpad: unmarshal: %w", err)
	}
	return entries, nil
}

// renderMarkdown produces a grouped human-readable Markdown table of entries.
func renderMarkdown(entries []ScratchpadEntry) string {
	type group struct {
		kind  ScratchpadKind
		label string
	}
	groups := []group{
		{KindInProgress, "In Progress"},
		{KindBlocked, "Blocked"},
		{KindDecision, "Decisions"},
		{KindHandoff, "Handoffs"},
		{KindWarning, "Warnings"},
	}

	// bucket by kind
	bucket := make(map[ScratchpadKind][]ScratchpadEntry, len(groups))
	for _, e := range entries {
		bucket[e.Kind] = append(bucket[e.Kind], e)
	}
	// sort each bucket newest first
	for k := range bucket {
		sort.Slice(bucket[k], func(i, j int) bool {
			return bucket[k][i].Timestamp.After(bucket[k][j].Timestamp)
		})
	}

	var sb strings.Builder
	for _, g := range groups {
		list := bucket[g.kind]
		if len(list) == 0 {
			continue
		}
		sb.WriteString("## ")
		sb.WriteString(g.label)
		sb.WriteString("\n\n")
		sb.WriteString("| Time | Agent | Task | Message |\n")
		sb.WriteString("|------|-------|------|---------|\n")
		for _, e := range list {
			sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				e.Timestamp.Format("2006-01-02 15:04"),
				e.AgentID,
				e.TaskID,
				strings.ReplaceAll(e.Message, "|", "\\|"),
			))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
