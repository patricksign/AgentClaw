package state

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	// maxSkillLines is the threshold: if a skill's rendered Markdown exceeds
	// this many lines, its body is split into a separate detail file under
	// skills/parts/<skill-id>.md, and the index only keeps a summary + link.
	maxSkillLines = 150

	// maxSkillsPerRole caps the number of skills retained per role.
	// Beyond this, the lowest-scoring skills are pruned.
	maxSkillsPerRole = 30

	// maxContextTokens is the approximate token budget (~4 chars/token)
	// for the skill section injected into the system prompt.
	maxContextChars = 3200 // ~800 tokens

	// maxRelevantSkills is how many detailed skills to load per task.
	// Keeps the multi-part buffer bounded.
	maxRelevantSkills = 8
)

// ─── Skill types ──────────────────────────────────────────────────────────────

// Skill represents a learned capability or pattern for a specific role.
// Skills whose rendered content exceeds maxSkillLines have their body
// written to a separate detail file; the index keeps only the summary.
type Skill struct {
	ID          string   `json:"id"`
	Role        string   `json:"role"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`               // ≤3 lines, always in index
	Description string   `json:"description,omitempty"`  // full text (empty if split to file)
	Pattern     string   `json:"pattern,omitempty"`      // what to do
	AntiPattern string   `json:"anti_pattern,omitempty"` // what NOT to do
	Examples    []string `json:"examples,omitempty"`      // concrete usage examples
	Tags        []string `json:"tags,omitempty"`          // keywords for relevance matching

	// DetailFile is the relative path under skills/parts/ where the full
	// body lives. Empty when the skill fits in the index (≤150 lines).
	DetailFile string `json:"detail_file,omitempty"`

	// Scoring fields — updated after each task.
	SuccessRate float64   `json:"success_rate"` // EMA, 0.0–1.0
	UsageCount  int       `json:"usage_count"`
	LearnedFrom string    `json:"learned_from"` // task ID
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SkillSet is the index file for one role: skills/<role>.json.
type SkillSet struct {
	Role      string    `json:"role"`
	Skills    []Skill   `json:"skills"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PostTaskReflection is produced by an agent after completing a task.
type PostTaskReflection struct {
	TaskID         string    `json:"task_id"`
	AgentID        string    `json:"agent_id"`
	Role           string    `json:"role"`
	Success        bool      `json:"success"`
	LessonsLearned []string  `json:"lessons_learned"`
	NewPatterns    []string  `json:"new_patterns"`
	AntiPatterns   []string  `json:"anti_patterns"`
	SkillsUsed     []string  `json:"skills_used"`
	SkillsToUpdate []string  `json:"skills_to_update"`
	CostUSD        float64   `json:"cost_usd"`
	InputTokens    int64     `json:"input_tokens"`
	OutputTokens   int64     `json:"output_tokens"`
	CacheHitTokens int64     `json:"cache_hit_tokens"`
	DurationMs     int64     `json:"duration_ms"`
	Timestamp      time.Time `json:"timestamp"`
}

// SkillQuery describes what the agent needs so the loader can select
// the most relevant skills for the current task.
type SkillQuery struct {
	Role       string   // agent role
	TaskTitle  string   // current task title
	TaskTags   []string // current task tags
	Complexity string   // S | M | L — larger = more skills loaded
}

// ─── SkillStore ──────────────────────────────────────────────────────────────

// Directory layout:
//
//	skills/
//	  <role>.json            — index (summaries + metadata, always loaded)
//	  parts/
//	    <skill-id>.md        — detail body (loaded on demand per task)
//	  previous/
//	    skills-<role>-<ts>.json — archived index versions
type SkillStore struct {
	mu          sync.RWMutex
	dir         string // skills/
	partsDir    string // skills/parts/
	previousDir string // skills/previous/
	cache       map[string]*SkillSet
}

// NewSkillStore creates the directory tree and returns a ready store.
func NewSkillStore(baseDir string) (*SkillStore, error) {
	partsDir := filepath.Join(baseDir, "parts")
	prevDir := filepath.Join(baseDir, "previous")
	for _, d := range []string{partsDir, prevDir} {
		if err := os.MkdirAll(d, 0750); err != nil {
			return nil, fmt.Errorf("skills: mkdir %s: %w", d, err)
		}
	}
	return &SkillStore{
		dir:         baseDir,
		partsDir:    partsDir,
		previousDir: prevDir,
		cache:       make(map[string]*SkillSet),
	}, nil
}

// ─── Load (index only) ──────────────────────────────────────────────────────

// Load reads the skill index for a role. Detail bodies are NOT loaded here;
// use LoadRelevant for task-specific multi-part loading.
func (s *SkillStore) Load(role string) (*SkillSet, error) {
	if err := validateAgentID(role); err != nil {
		return nil, fmt.Errorf("skills: invalid role: %w", err)
	}

	// Use a single Lock for the entire load-from-disk-and-cache path to prevent
	// concurrent loaders from overwriting a fresher cache entry with stale data (#58).
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.cache[role]; ok {
		return cached, nil
	}

	path := filepath.Join(s.dir, role+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SkillSet{Role: role, Version: 0, UpdatedAt: time.Now()}, nil
		}
		return nil, fmt.Errorf("skills: read %s: %w", role, err)
	}

	var ss SkillSet
	if err := json.Unmarshal(data, &ss); err != nil {
		return nil, fmt.Errorf("skills: parse %s: %w", role, err)
	}

	s.cache[role] = &ss
	return &ss, nil
}

// ─── LoadRelevant (multi-part buffer) ────────────────────────────────────────

// LoadRelevant returns the most relevant skills for the given task query.
// It uses a two-step process:
//  1. Load the index and score every skill against the query.
//  2. For the top-N skills, load their detail files from parts/ if they
//     were split (DetailFile != "").
//
// This keeps memory bounded: the index is always small, and only the
// skills needed for THIS task have their full body loaded.
func (s *SkillStore) LoadRelevant(q SkillQuery) ([]Skill, error) {
	ss, err := s.Load(q.Role)
	if err != nil {
		return nil, err
	}
	if len(ss.Skills) == 0 {
		return nil, nil
	}

	// Determine how many detail skills to load based on complexity.
	limit := maxRelevantSkills
	switch q.Complexity {
	case "S":
		limit = 4
	case "M":
		limit = 6
	case "L":
		limit = maxRelevantSkills
	}

	// Precompute query data once — avoids rebuilding per-skill (M3/M4 fix).
	pq := prepareQuery(q)

	// Score and rank.
	type scored struct {
		idx   int
		score float64
	}
	ranked := make([]scored, 0, len(ss.Skills))
	for i := range ss.Skills {
		sc := relevanceScore(&ss.Skills[i], pq)
		ranked = append(ranked, scored{idx: i, score: sc})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	// Load detail bodies for selected skills.
	result := make([]Skill, 0, len(ranked))
	for _, r := range ranked {
		sk := ss.Skills[r.idx] // copy
		if sk.DetailFile != "" {
			body, rerr := s.loadDetail(sk.DetailFile)
			if rerr != nil {
				log.Warn().Err(rerr).Str("skill", sk.ID).Msg("skills: load detail failed, using summary")
			} else {
				sk.Description = body
			}
		}
		result = append(result, sk)
	}

	return result, nil
}

// loadDetail reads a detail file from parts/.
// Validates the resolved path stays within partsDir to prevent path traversal.
func (s *SkillStore) loadDetail(relPath string) (string, error) {
	path := filepath.Join(s.partsDir, relPath)
	if !isSubPath(s.partsDir, path) {
		return "", fmt.Errorf("skills: path traversal blocked: %q", relPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("skills: read detail %s: %w", relPath, err)
	}
	return string(data), nil
}

// isSubPath returns true if child is inside parent after cleaning both paths.
func isSubPath(parent, child string) bool {
	cleanParent := filepath.Clean(parent) + string(os.PathSeparator)
	cleanChild := filepath.Clean(child)
	return strings.HasPrefix(cleanChild, cleanParent)
}

// ─── Relevance scoring (standalone functions, no SkillStore receiver) ────────

// preparedQuery holds precomputed data from SkillQuery so that per-skill
// scoring avoids redundant allocations (M3/M4/M5 fix).
type preparedQuery struct {
	taskTagSet    map[string]struct{} // lowercased task tags for O(1) lookup
	titleKeywords map[string]struct{} // lowercased title keywords for O(1) lookup
}

// prepareQuery precomputes query data once before the scoring loop.
func prepareQuery(q SkillQuery) preparedQuery {
	pq := preparedQuery{}

	// Precompute tag set (M3).
	if len(q.TaskTags) > 0 {
		pq.taskTagSet = make(map[string]struct{}, len(q.TaskTags))
		for _, t := range q.TaskTags {
			pq.taskTagSet[strings.ToLower(t)] = struct{}{}
		}
	}

	// Precompute title keywords as set (M4+M5: avoids O(n²) nested loop).
	if q.TaskTitle != "" {
		words := extractKeywords(q.TaskTitle)
		pq.titleKeywords = make(map[string]struct{}, len(words))
		for _, w := range words {
			if len(w) > 3 { // skip short words
				pq.titleKeywords[w] = struct{}{}
			}
		}
	}

	return pq
}

// relevanceScore computes how relevant a skill is to the current task.
// Uses preparedQuery to avoid per-skill allocations.
//
// Scoring formula:
//
//	base       = successRate × 0.25 + usageFrequency × 0.15 + recency × 0.10
//	tagBonus   = +0.25 per matching tag (capped at 0.50)
//	titleBonus = +0.15 per keyword overlap with task title (capped at 0.30)
//	antiBonus  = +0.20 for anti-patterns (always somewhat relevant)
func relevanceScore(sk *Skill, pq preparedQuery) float64 {
	// Base quality score.
	usage := math.Min(float64(sk.UsageCount), 20) / 20
	daysSince := time.Since(sk.UpdatedAt).Hours() / 24
	recency := 1.0
	if daysSince > 30 {
		recency = 30.0 / daysSince
	}
	base := sk.SuccessRate*0.25 + usage*0.15 + recency*0.10

	// Tag overlap bonus — O(len(sk.Tags)) with precomputed set.
	tagBonus := 0.0
	if len(sk.Tags) > 0 && len(pq.taskTagSet) > 0 {
		for _, t := range sk.Tags {
			if _, ok := pq.taskTagSet[strings.ToLower(t)]; ok {
				tagBonus += 0.25
			}
		}
		if tagBonus > 0.50 {
			tagBonus = 0.50
		}
	}

	// Title keyword overlap — O(len(skillWords)) with precomputed set (M5 fix).
	titleBonus := 0.0
	if len(pq.titleKeywords) > 0 {
		skillWords := extractKeywords(sk.Title + " " + sk.Summary)
		for _, sw := range skillWords {
			if _, ok := pq.titleKeywords[sw]; ok {
				titleBonus += 0.15
			}
		}
		if titleBonus > 0.30 {
			titleBonus = 0.30
		}
	}

	// Anti-patterns get a baseline bonus — always somewhat relevant.
	antiBonus := 0.0
	if sk.AntiPattern != "" {
		antiBonus = 0.20
	}

	return base + tagBonus + titleBonus + antiBonus
}

// extractKeywords splits text into lowercase words, deduped.
func extractKeywords(text string) []string {
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
	})
	seen := make(map[string]struct{}, len(words))
	result := make([]string, 0, len(words))
	for _, w := range words {
		if _, exists := seen[w]; !exists {
			seen[w] = struct{}{}
			result = append(result, w)
		}
	}
	return result
}

// ─── Save (with auto-split) ─────────────────────────────────────────────────

// Save persists the skill set. For each skill whose rendered content exceeds
// maxSkillLines, the body is written to parts/<skill-id>.md and the index
// only keeps the summary + DetailFile reference.
//
// Before writing, the previous index version is archived.
func (s *SkillStore) Save(ss *SkillSet) error {
	if err := validateAgentID(ss.Role); err != nil {
		return fmt.Errorf("skills: invalid role: %w", err)
	}

	// Archive previous index.
	indexPath := filepath.Join(s.dir, ss.Role+".json")
	if _, err := os.Stat(indexPath); err == nil {
		ts := time.Now().Format("20060102-150405")
		archName := fmt.Sprintf("skills-%s-%s.json", ss.Role, ts)
		archPath := filepath.Join(s.previousDir, archName)
		if data, rerr := os.ReadFile(indexPath); rerr == nil {
			if werr := os.WriteFile(archPath, data, 0640); werr != nil {
				log.Warn().Err(werr).Str("role", ss.Role).Msg("skills: archive failed")
			}
		}
	}

	// Auto-split oversized skills.
	for i := range ss.Skills {
		if err := s.autoSplit(&ss.Skills[i]); err != nil {
			return fmt.Errorf("skills: auto-split %s: %w", ss.Skills[i].ID, err)
		}
	}

	ss.UpdatedAt = time.Now()
	ss.Version++

	data, err := json.MarshalIndent(ss, "", "  ")
	if err != nil {
		return fmt.Errorf("skills: marshal %s: %w", ss.Role, err)
	}

	tmpPath := indexPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0640); err != nil {
		return fmt.Errorf("skills: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, indexPath); err != nil {
		return fmt.Errorf("skills: rename %s: %w", indexPath, err)
	}

	s.mu.Lock()
	s.cache[ss.Role] = ss
	s.mu.Unlock()

	return nil
}

// autoSplit checks if a skill's content exceeds maxSkillLines.
// If so, writes the full body to parts/<skill-id>.md and clears the
// inline fields, keeping only Summary and DetailFile.
// Returns an error if the detail file cannot be written.
func (s *SkillStore) autoSplit(sk *Skill) error {
	rendered := renderSkillBody(sk)
	lineCount := strings.Count(rendered, "\n") + 1

	if lineCount <= maxSkillLines {
		// Fits in index — ensure DetailFile is cleared and summary is populated.
		sk.DetailFile = ""
		if sk.Summary == "" {
			sk.Summary = buildSummary(sk)
		}
		return nil
	}

	// Validate skill ID is safe for use as filename (no path traversal).
	if !agentIDRe.MatchString(sk.ID) {
		return fmt.Errorf("skills: unsafe skill ID for filename: %q", sk.ID)
	}

	// Write full body to detail file using tmp+rename for atomicity.
	filename := sk.ID + ".md"
	detailPath := filepath.Join(s.partsDir, filename)
	if !isSubPath(s.partsDir, detailPath) {
		return fmt.Errorf("skills: path traversal blocked for skill ID: %q", sk.ID)
	}
	tmpPath := detailPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0640); err != nil {
		return fmt.Errorf("skills: write detail %s: %w", sk.ID, err)
	}
	if err := os.Rename(tmpPath, detailPath); err != nil {
		return fmt.Errorf("skills: rename detail %s: %w", sk.ID, err)
	}

	// Keep only summary in the index.
	sk.DetailFile = filename
	if sk.Summary == "" {
		sk.Summary = buildSummary(sk)
	}
	// Clear inline body — it lives in the detail file now.
	sk.Description = ""
	sk.Pattern = ""
	sk.AntiPattern = ""
	sk.Examples = nil
	return nil
}

// renderSkillBody produces the full Markdown body of a skill.
func renderSkillBody(sk *Skill) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", sk.Title))

	if sk.Summary != "" {
		sb.WriteString(fmt.Sprintf("**Summary:** %s\n\n", sk.Summary))
	}

	if sk.Description != "" {
		sb.WriteString("## Description\n\n")
		sb.WriteString(sk.Description)
		sb.WriteString("\n\n")
	}

	if sk.Pattern != "" {
		sb.WriteString("## Pattern (Do)\n\n")
		sb.WriteString(sk.Pattern)
		sb.WriteString("\n\n")
	}

	if sk.AntiPattern != "" {
		sb.WriteString("## Anti-Pattern (Don't)\n\n")
		sb.WriteString(sk.AntiPattern)
		sb.WriteString("\n\n")
	}

	if len(sk.Examples) > 0 {
		sb.WriteString("## Examples\n\n")
		for i, ex := range sk.Examples {
			sb.WriteString(fmt.Sprintf("### Example %d\n\n%s\n\n", i+1, ex))
		}
	}

	sb.WriteString(fmt.Sprintf("---\n*Role: %s | Success: %.0f%% | Used: %d times | From: %s*\n",
		sk.Role, sk.SuccessRate*100, sk.UsageCount, sk.LearnedFrom))

	return sb.String()
}

// buildSummary generates a ≤3 line summary from the skill's fields.
func buildSummary(sk *Skill) string {
	if sk.AntiPattern != "" {
		return "AVOID: " + truncateStr(sk.AntiPattern, 120)
	}
	if sk.Pattern != "" {
		return truncateStr(sk.Pattern, 120)
	}
	return truncateStr(sk.Description, 120)
}

// ─── ApplyReflection ─────────────────────────────────────────────────────────

// ApplyReflection updates the skill set based on a PostTaskReflection.
func (s *SkillStore) ApplyReflection(reflection PostTaskReflection) error {
	ss, err := s.Load(reflection.Role)
	if err != nil {
		return fmt.Errorf("skills: load for reflection: %w", err)
	}

	// Index existing skills by ID.
	skillIdx := make(map[string]int, len(ss.Skills))
	for i, sk := range ss.Skills {
		skillIdx[sk.ID] = i
	}

	// Update used skills.
	for _, usedID := range reflection.SkillsUsed {
		if idx, ok := skillIdx[usedID]; ok {
			sk := &ss.Skills[idx]
			sk.UsageCount++
			alpha := 0.3
			successVal := 0.0
			if reflection.Success {
				successVal = 1.0
			}
			sk.SuccessRate = sk.SuccessRate*(1-alpha) + successVal*alpha
			sk.UpdatedAt = time.Now()
		}
	}

	now := time.Now()
	nextID := len(ss.Skills) + 1

	// Build base tags from the task context.
	// Each skill gets its own copy to prevent slice aliasing corruption.
	baseTags := []string{reflection.Role}
	if reflection.TaskID != "" {
		baseTags = append(baseTags, reflection.TaskID)
	}

	// copyTags creates a fresh slice from baseTags + extra tag.
	// This prevents append from sharing backing arrays across skills.
	copyTags := func(extra string) []string {
		tags := make([]string, len(baseTags)+1)
		copy(tags, baseTags)
		tags[len(baseTags)] = extra
		return tags
	}

	// New skills from lessons learned.
	for _, lesson := range reflection.LessonsLearned {
		if strings.TrimSpace(lesson) == "" {
			continue
		}
		id := fmt.Sprintf("skill-%s-%d-%s", reflection.Role, nextID, shortHash(lesson))
		nextID++
		ss.Skills = append(ss.Skills, Skill{
			ID:          id,
			Role:        reflection.Role,
			Title:       truncateSkillTitle(lesson),
			Summary:     truncateStr(lesson, 120),
			Description: lesson,
			Tags:        copyTags("lesson"),
			SuccessRate: 0.5,
			LearnedFrom: reflection.TaskID,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// New patterns.
	for _, pattern := range reflection.NewPatterns {
		if strings.TrimSpace(pattern) == "" {
			continue
		}
		id := fmt.Sprintf("skill-%s-%d-%s", reflection.Role, nextID, shortHash(pattern))
		nextID++
		ss.Skills = append(ss.Skills, Skill{
			ID:          id,
			Role:        reflection.Role,
			Title:       truncateSkillTitle(pattern),
			Summary:     truncateStr(pattern, 120),
			Pattern:     pattern,
			Tags:        copyTags("pattern"),
			SuccessRate: 0.7,
			LearnedFrom: reflection.TaskID,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// Anti-patterns.
	for _, ap := range reflection.AntiPatterns {
		if strings.TrimSpace(ap) == "" {
			continue
		}
		id := fmt.Sprintf("skill-%s-%d-%s", reflection.Role, nextID, shortHash(ap))
		nextID++
		ss.Skills = append(ss.Skills, Skill{
			ID:          id,
			Role:        reflection.Role,
			Title:       "AVOID: " + truncateSkillTitle(ap),
			Summary:     "AVOID: " + truncateStr(ap, 110),
			AntiPattern: ap,
			Tags:        copyTags("anti-pattern"),
			SuccessRate: 0.0,
			LearnedFrom: reflection.TaskID,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	// Prune.
	ss.Skills = pruneSkills(ss.Skills, maxSkillsPerRole)

	return s.Save(ss)
}

// ─── BuildSkillContext ────────────────────────────────────────────────────────

// BuildSkillContext returns a Markdown section for injection into the system
// prompt. It loads ALL skills (summaries only) as a quick overview, without
// loading any detail files. Use BuildSkillContextForTask for task-specific
// detailed loading.
func (s *SkillStore) BuildSkillContext(role string) string {
	return s.BuildSkillContextForTask(SkillQuery{
		Role:       role,
		Complexity: "M",
	})
}

// BuildSkillContextForTask loads relevant skills for the given task query
// using the multi-part buffer, then renders them into a bounded Markdown
// section that fits within the token budget.
//
// Structure of the rendered context:
//
//	## LEARNED SKILLS
//	### Quick Reference (all skills, summaries only)
//	- [skill-id] title (success%, used N times)
//	### Detailed Skills (top-N relevant to this task)
//	#### skill title
//	full body loaded from parts/ if split
func (s *SkillStore) BuildSkillContextForTask(q SkillQuery) string {
	ss, err := s.Load(q.Role)
	if err != nil || len(ss.Skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## LEARNED SKILLS (from previous tasks)\n\n")

	// ── Section 1: Quick reference (summaries only, all skills) ──────────
	sb.WriteString("### Quick Reference\n\n")

	// Partition into do/don't by index to avoid copying large Skill structs (M6).
	var doIdx, dontIdx []int
	for i := range ss.Skills {
		if ss.Skills[i].AntiPattern != "" || strings.HasPrefix(ss.Skills[i].Title, "AVOID:") {
			dontIdx = append(dontIdx, i)
		} else {
			doIdx = append(doIdx, i)
		}
	}

	if len(doIdx) > 0 {
		sb.WriteString("**Do:**\n")
		for _, i := range doIdx {
			sk := &ss.Skills[i]
			summary := sk.Summary
			if summary == "" {
				summary = sk.Title
			}
			sb.WriteString("- `")
			sb.WriteString(sk.ID)
			sb.WriteString("` ")
			sb.WriteString(summary)
			sb.WriteString(fmt.Sprintf(" (%.0f%%, ×%d)", sk.SuccessRate*100, sk.UsageCount))
			if sk.DetailFile != "" {
				sb.WriteString(" 📎")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if len(dontIdx) > 0 {
		sb.WriteString("**Don't:**\n")
		for _, i := range dontIdx {
			sk := &ss.Skills[i]
			summary := sk.Summary
			if summary == "" {
				summary = sk.Title
			}
			sb.WriteString("- `")
			sb.WriteString(sk.ID)
			sb.WriteString("` ")
			sb.WriteString(summary)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// ── Section 2: Detailed skills (multi-part buffer load) ──────────────
	relevant, loadErr := s.LoadRelevant(q)
	if loadErr != nil {
		log.Warn().Err(loadErr).Str("role", q.Role).Msg("skills: LoadRelevant failed")
	}

	// Filter to only skills that have meaningful detail beyond the summary.
	var detailed []Skill
	for _, sk := range relevant {
		hasDetail := sk.Description != "" || sk.Pattern != "" || sk.AntiPattern != "" || len(sk.Examples) > 0
		if hasDetail {
			detailed = append(detailed, sk)
		}
	}

	if len(detailed) > 0 {
		sb.WriteString("### Detailed (most relevant to this task)\n\n")
		budgetLeft := maxContextChars - sb.Len()

		for _, sk := range detailed {
			if budgetLeft <= 200 {
				sb.WriteString("\n…(budget exhausted, see Quick Reference for more)\n")
				break
			}

			entry := renderSkillEntry(sk)
			if len(entry) > budgetLeft {
				// Truncate this entry to fit.
				entry = entry[:budgetLeft-20] + "\n…(truncated)\n"
			}
			sb.WriteString(entry)
			budgetLeft -= len(entry)
		}
	}

	return sb.String()
}

// renderSkillEntry renders a single skill for the Detailed section.
func renderSkillEntry(sk Skill) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("#### %s\n", sk.Title))

	if sk.Description != "" {
		sb.WriteString(sk.Description + "\n")
	}
	if sk.Pattern != "" {
		sb.WriteString("**Pattern:** " + sk.Pattern + "\n")
	}
	if sk.AntiPattern != "" {
		sb.WriteString("**Avoid:** " + sk.AntiPattern + "\n")
	}
	if len(sk.Examples) > 0 {
		sb.WriteString("**Examples:**\n")
		for _, ex := range sk.Examples {
			sb.WriteString("- " + truncateStr(ex, 200) + "\n")
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func truncateSkillTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// shortHash returns the first 6 hex chars of SHA-256, used for skill ID uniqueness.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:3])
}

// pruneSkills keeps the top maxN skills by composite score.
// Anti-patterns are always kept.
func pruneSkills(skills []Skill, maxN int) []Skill {
	if len(skills) <= maxN {
		return skills
	}

	now := time.Now()
	type scored struct {
		idx   int
		score float64
	}

	ranked := make([]scored, 0, len(skills))
	for i := range skills {
		sk := &skills[i] // pointer avoids copying large Skill struct (M6)
		if sk.AntiPattern != "" {
			ranked = append(ranked, scored{idx: i, score: 100})
			continue
		}
		usage := math.Min(float64(sk.UsageCount), 10) / 10
		daysSince := now.Sub(sk.UpdatedAt).Hours() / 24
		recency := 1.0
		if daysSince > 30 {
			recency = 30.0 / daysSince
		}
		score := sk.SuccessRate*0.5 + usage*0.3 + recency*0.2
		ranked = append(ranked, scored{idx: i, score: score})
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > maxN {
		ranked = ranked[:maxN]
	}

	result := make([]Skill, len(ranked))
	for i, r := range ranked {
		result[i] = skills[r.idx]
	}
	return result
}
