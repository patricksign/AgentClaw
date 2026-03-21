package memory

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/state"
	"github.com/rs/zerolog/log"
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
func (s *Store) BuildContext(agentID, role, taskTitle, complexity string) agent.MemoryContext {
	if complexity == "" {
		complexity = "M"
	}
	ctx := agent.MemoryContext{}

	// ── Tier 1 — always loaded ────────────────────────────────────────────

	// AgentDoc: role-specific conventions and pitfalls (cap 800 tokens).
	if s.agentDoc != nil {
		if doc, derr := s.agentDoc.Read(role); derr == nil {
			ctx.AgentDoc = truncateToTokens(doc, 800)
		} else {
			log.Warn().Err(derr).Str("role", role).Msg("BuildContext: AgentDoc.Read failed")
		}
	}

	// ScopeManifest: what this agent owns / must not touch.
	if s.scope != nil {
		if m, serr := s.scope.Read(role); serr != nil {
			log.Warn().Err(serr).Str("role", role).Msg("BuildContext: scope.Read failed")
		} else {
			ctx.Scope = m
		}
	}

	// Scratchpad: compact last-24 h team status (cap 400 tokens).
	ctx.Scratchpad = s.scratchpad

	// ── Tier 2 — M or L ──────────────────────────────────────────────────

	// Read project doc once; tier determines the token cap applied below.
	rawProjectDoc := s.ReadProjectDoc()

	if complexity == "M" || complexity == "L" {
		// RecentByRole: fetch 5 for L, 3 for M — single query avoids redundant DB call.
		recentLimit := 3
		if complexity == "L" {
			recentLimit = 5
		}
		recent, err := s.RecentByRole(role, recentLimit)
		if err != nil {
			log.Warn().Err(err).Str("role", role).Msg("BuildContext: RecentByRole failed")
		} else {
			for _, t := range recent {
				t.Lock()
				title, desc := t.Title, t.Description
				status := t.Status
				t.Unlock()
				entry := truncateToTokens(fmt.Sprintf("[%s] %s: %s", status, title, desc), 300)
				ctx.RelevantCode = append(ctx.RelevantCode, entry)
			}
			ctx.RecentTasks = recent
		}

		// ResolvedStore: top-3 matching error patterns (cap 200 tokens each).
		if s.resolved != nil {
			if matches, serr := s.resolved.Search(taskTitle, role); serr == nil {
				top := matches
				if len(top) > 3 {
					top = top[:3]
				}
				for _, m := range top {
					snippet := truncateToTokens(
						fmt.Sprintf("**%s** (seen %d×)\nFix: %s", m.ErrorPattern, m.OccurrenceCount, m.ResolutionSummary),
						200,
					)
					ctx.RelevantCode = append(ctx.RelevantCode, snippet)
				}
			}
		}

		// Project doc: first 800 tokens only (M), full 2000 tokens (L).
		if complexity == "L" {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 2000)
		} else {
			ctx.ProjectDoc = truncateToTokens(rawProjectDoc, 800)
		}
	}

	// ── Tier 3 — L only ──────────────────────────────────────────────────

	if complexity == "L" {

		// Cross-agent awareness via ScopeStore.ReadAll().
		if s.scope != nil {
			if all, aerr := s.scope.ReadAll(); aerr != nil {
				log.Warn().Err(aerr).Msg("BuildContext: ScopeStore.ReadAll failed")
			} else {
				for i := range all {
					ctx.AllScopes = append(ctx.AllScopes, &all[i])
				}
			}
		}

		// ADRs are only loaded at tier 3 to keep lower tiers lean.
		adrs, err := s.ListADRs()
		if err != nil {
			log.Warn().Err(err).Msg("BuildContext: ListADRs failed")
		} else {
			ctx.ADRs = adrs
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
