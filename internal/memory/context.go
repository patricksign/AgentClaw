package memory

import (
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/patricksign/AgentClaw/internal/adapter"
	"github.com/patricksign/AgentClaw/internal/state"
)

func New(dbPath, projectPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, err
	}
	// WAL mode supports concurrent readers with a single writer.
	// With 9 role workers + summarizer + API handlers, we need enough
	// read connections to avoid blocking. SQLite serialises writes
	// internally via _busy_timeout.
	//
	// _synchronous=NORMAL is safe with WAL — data is durable against
	// application crashes (only OS crash can lose last txn, acceptable
	// for task metadata).
	// 9 role workers + cron + API handlers + pipeline goroutines need concurrent reads.
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute) // recycle connections to avoid stale state

	s := &Store{db: db, projectPath: projectPath}
	return s, s.migrate()
}

// NewWithState creates a Store and attaches a ResolvedStore rooted at stateBaseDir.
// If stateBaseDir is empty, the ResolvedStore is not initialised (Resolved() returns nil).
func NewWithState(dbPath, projectPath, stateBaseDir string) (*Store, error) {
	s, err := New(dbPath, projectPath)
	if err != nil {
		return nil, err
	}
	if stateBaseDir != "" {
		rs, rerr := state.NewResolvedStore(stateBaseDir)
		if rerr != nil {
			return nil, fmt.Errorf("memory: init resolved store: %w", rerr)
		}
		s.resolved = rs

		ss, serr := state.NewScopeStore(stateBaseDir)
		if serr != nil {
			return nil, fmt.Errorf("memory: init scope store: %w", serr)
		}
		s.scope = ss

		// Derive memoryBaseDir from stateBaseDir (sibling directory).
		memoryBaseDir := filepath.Join(filepath.Dir(stateBaseDir), "memory")
		ads, aerr := state.NewAgentDocStore(memoryBaseDir)
		if aerr != nil {
			return nil, fmt.Errorf("memory: init agent doc store: %w", aerr)
		}
		s.agentDoc = ads

		sp, serr2 := state.NewScratchpad(stateBaseDir)
		if serr2 != nil {
			return nil, fmt.Errorf("memory: init scratchpad: %w", serr2)
		}
		s.scratchpad = sp
	}
	return s, nil
}

// BuildContext assembles a tiered MemoryContext based on task complexity.
//
// Complexity "S" — Tier 1 only (~500 tokens): AgentDoc, ScopeManifest, Scratchpad.
// Complexity "M" — Tier 1 + Tier 2 (~1500 tokens): adds RecentByRole(3),
//
//	ResolvedStore top-3 matches, first 800 chars of project.md.
//
// Complexity "L" — all tiers (~3000 tokens): RecentByRole(5), full
//
//	project.md, ScopeStore.ReadAll() for cross-agent awareness.
//
// After assembly, enforceTokenBudget trims if total exceeds the tier budget
// (S=2000, M=6000, L=12000 tokens).
//
// If complexity is empty it defaults to "M".
func (s *Store) BuildContext(agentID, role, taskTitle, complexity string) adapter.MemoryContext {
	if complexity == "" {
		complexity = "M"
	}
	ctx := adapter.MemoryContext{}

	// ── Tier 1 — always loaded ────────────────────────────────────────────

	// AgentDoc: role-specific conventions and pitfalls (cap 800 tokens).
	if s.agentDoc != nil {
		if doc, derr := s.agentDoc.Read(role); derr == nil {
			ctx.AgentDoc = truncateToTokens(doc, 800)
		} else {
			slog.Warn("BuildContext: AgentDoc.Read failed", "err", derr, "role", role)
		}
	}

	// ScopeManifest: what this agent owns / must not touch.
	if s.scope != nil {
		if m, serr := s.scope.Read(role); serr != nil {
			slog.Warn("BuildContext: scope.Read failed", "err", serr, "role", role)
		} else {
			ctx.Scope = m
		}
	}

	// Scratchpad: compact last-24 h team status (cap 400 tokens).
	ctx.Scratchpad = s.scratchpad

	// ── Tier 2+3 — parallel I/O for M and L ─────────────────────────────
	// SQLite WAL mode supports concurrent readers. Independent queries run
	// in parallel to reduce BuildContext latency (especially for L-complexity).

	if complexity == "M" || complexity == "L" {
		var wg sync.WaitGroup
		var mu sync.Mutex // protects ctx fields

		recentLimit := 3
		if complexity == "L" {
			recentLimit = 5
		}

		// Parallel: project doc (file I/O)
		var rawProjectDoc string
		wg.Add(1)
		go func() {
			defer wg.Done()
			rawProjectDoc = s.ReadProjectDoc()
		}()

		// Parallel: recent tasks (DB query)
		wg.Add(1)
		go func() {
			defer wg.Done()
			recent, err := s.RecentByRole(role, recentLimit)
			if err != nil {
				slog.Warn("BuildContext: RecentByRole failed", "err", err, "role", role)
				return
			}
			var entries []string
			for _, t := range recent {
				t.Lock()
				title, desc, status := t.Title, t.Description, t.Status
				t.Unlock()
				entries = append(entries, truncateToTokens(fmt.Sprintf("[%s] %s: %s", status, title, desc), 300))
			}
			mu.Lock()
			ctx.RelevantCode = append(ctx.RelevantCode, entries...)
			ctx.RecentTasks = recent
			mu.Unlock()
		}()

		// Parallel: resolved error patterns (file I/O + search)
		if s.resolved != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				matches, serr := s.resolved.Search(taskTitle, role)
				if serr != nil {
					return
				}
				if len(matches) > 3 {
					matches = matches[:3]
				}
				var snippets []string
				for _, m := range matches {
					snippets = append(snippets, truncateToTokens(
						fmt.Sprintf("**%s** (seen %d×)\nFix: %s", m.ErrorPattern, m.OccurrenceCount, m.ResolutionSummary),
						200,
					))
				}
				mu.Lock()
				ctx.RelevantCode = append(ctx.RelevantCode, snippets...)
				mu.Unlock()
			}()
		}

		// Parallel (L only): scope.ReadAll + ListADRs
		if complexity == "L" {
			if s.scope != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					all, aerr := s.scope.ReadAll()
					if aerr != nil {
						slog.Warn("BuildContext: ScopeStore.ReadAll failed", "err", aerr)
						return
					}
					mu.Lock()
					for i := range all {
						ctx.AllScopes = append(ctx.AllScopes, &all[i])
					}
					mu.Unlock()
				}()
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				adrs, err := s.ListADRs()
				if err != nil {
					slog.Warn("BuildContext: ListADRs failed", "err", err)
					return
				}
				mu.Lock()
				ctx.ADRs = adrs
				mu.Unlock()
			}()
		}

		wg.Wait()

		// Apply project doc cap (needs rawProjectDoc from goroutine).
		if complexity == "L" {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 2000)
		} else {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 800)
		}
	}

	// ResolvedStore reference — agents use it directly for runtime lookups.
	ctx.Resolved = s.resolved

	// ── Budget enforcement ──────────────────────────────────────────────
	budget := maxContextTokens[complexity]
	if budget == 0 {
		budget = maxContextTokens["M"]
	}
	enforceTokenBudget(&ctx, budget)

	return ctx
}
