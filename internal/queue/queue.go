package queue

import (
	"container/heap"
	"context"
	"sync"

	"github.com/patricksign/agentclaw/internal/agent"
)

// ─── Priority heap impl ───────────────────────────────────────────────────────

type item struct {
	task  *agent.Task
	index int
}

type pq []*item

func (q pq) Len() int           { return len(q) }
func (q pq) Less(i, j int) bool { return q[i].task.Priority > q[j].task.Priority } // max-heap
func (q pq) Swap(i, j int)      { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *pq) Push(x any)        { it := x.(*item); it.index = len(*q); *q = append(*q, it) }
func (q *pq) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil // avoid memory leak of pointer in backing array
	it.index = -1  // mark as removed so roleIndex skips stale entries
	*q = old[:n-1]
	return it
}

// ─── Queue ───────────────────────────────────────────────────────────────────

// maxDoneIDs caps the doneIDs set to prevent unbounded memory growth.
// When the cap is reached the oldest half is evicted.
const maxDoneIDs = 10_000

// Queue is a priority queue with dependency tracking and deduplication.
// Agents only receive a task when all its dependencies are done.
type Queue struct {
	mu         sync.Mutex
	heap       pq
	doneIDs    map[string]bool
	doneSeq    []string                // insertion-order list for eviction
	inQueue    map[string]bool         // dedup: task IDs currently in the heap
	roleIndex  map[string][]*item      // role → items for O(role_count) findReady
	notify     chan struct{}           // global fallback signal
	roleNotify map[string]chan struct{} // per-role notification channels
}

func New() *Queue {
	q := &Queue{
		doneIDs:    make(map[string]bool),
		inQueue:    make(map[string]bool),
		roleIndex:  make(map[string][]*item),
		notify:     make(chan struct{}, 1),
		roleNotify: make(map[string]chan struct{}),
	}
	heap.Init(&q.heap)
	return q
}

// roleChannel returns the notification channel for the given role, creating
// one if it does not exist. Caller must hold q.mu.
func (q *Queue) roleChannel(role string) chan struct{} {
	ch, ok := q.roleNotify[role]
	if !ok {
		ch = make(chan struct{}, 1)
		q.roleNotify[role] = ch
	}
	return ch
}

// Push adds a task to the queue. Duplicate task IDs (already in queue or
// already done) are silently dropped to prevent re-processing.
func (q *Queue) Push(task *agent.Task) {
	q.mu.Lock()
	// Dedup: skip if this task ID is already queued or already completed.
	if q.inQueue[task.ID] || q.doneIDs[task.ID] {
		q.mu.Unlock()
		return
	}
	q.inQueue[task.ID] = true
	it := &item{task: task}
	heap.Push(&q.heap, it)
	q.roleIndex[task.AgentRole] = append(q.roleIndex[task.AgentRole], it)
	roleCh := q.roleChannel(task.AgentRole)
	q.mu.Unlock()

	// Notify the role-specific worker first, then the global channel as fallback.
	select {
	case roleCh <- struct{}{}:
	default:
	}
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Pop blocks until a task matching role is ready (all deps done) or ctx is cancelled.
func (q *Queue) Pop(ctx context.Context, role string) (*agent.Task, error) {
	// Obtain role-specific channel under lock.
	q.mu.Lock()
	roleCh := q.roleChannel(role)
	q.mu.Unlock()

	for {
		q.mu.Lock()
		task := q.findReady(role)
		q.mu.Unlock()

		if task != nil {
			return task, nil
		}

		// Block until notified for this role, globally, or context cancelled.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-roleCh:
		case <-q.notify:
		}
	}
}

// MarkDone records a task as complete, unblocking any dependents.
// It notifies every role that has a waiting task whose dependency just became
// satisfied, so workers wake up promptly instead of waiting for the global
// fallback signal.
func (q *Queue) MarkDone(taskID string) {
	q.mu.Lock()
	q.recordDone(taskID)
	delete(q.inQueue, taskID)

	// Collect roles of tasks that were waiting on this taskID.
	var rolesToNotify []chan struct{}
	for i := 0; i < q.heap.Len(); i++ {
		t := q.heap[i].task
		for _, dep := range t.DependsOn {
			if dep == taskID {
				rolesToNotify = append(rolesToNotify, q.roleChannel(t.AgentRole))
				break
			}
		}
	}
	q.mu.Unlock()

	// Notify role-specific channels for all unblocked tasks.
	for _, ch := range rolesToNotify {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	// Global fallback — wakes any worker not matched above.
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// recordDone adds taskID to doneIDs with bounded eviction. Caller must hold mu.
func (q *Queue) recordDone(id string) {
	if q.doneIDs[id] {
		return
	}
	q.doneIDs[id] = true
	q.doneSeq = append(q.doneSeq, id)

	if len(q.doneSeq) > maxDoneIDs {
		// Evict the oldest half using copy to release the backing array.
		evict := q.doneSeq[:maxDoneIDs/2]
		for _, eid := range evict {
			delete(q.doneIDs, eid)
		}
		newSeq := make([]string, len(q.doneSeq)-maxDoneIDs/2)
		copy(newSeq, q.doneSeq[maxDoneIDs/2:])
		q.doneSeq = newSeq
	}
}

// MarkFailed re-enqueues the task if retries remain.
// maxRetries must be a fixed value — do NOT pass task.Retries+N.
func (q *Queue) MarkFailed(task *agent.Task, maxRetries int) {
	task.Lock()
	task.Retries++
	shouldRetry := task.Retries <= maxRetries
	if shouldRetry {
		task.Status = agent.TaskQueued
	}
	task.Unlock()

	if shouldRetry {
		// Clear from inQueue so Push accepts the retry.
		q.mu.Lock()
		delete(q.inQueue, task.ID)
		q.mu.Unlock()
		q.Push(task)
	}
	// else: drop permanently (caller should log this)
}

// Len returns the number of waiting tasks.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.heap.Len()
}

// findReady finds the highest-priority task that:
//  1. Matches the given role (empty role = accept any)
//  2. Has all dependencies done
//
// When a role is specified, uses the roleIndex to scan only tasks for that role
// instead of the entire heap — O(role_count) instead of O(n).
// Must be called under mu.Lock.
func (q *Queue) findReady(role string) *agent.Task {
	var candidates []*item
	if role != "" {
		candidates = q.roleIndex[role]
	} else {
		candidates = []*item(q.heap)
	}

	bestIdx := -1
	var bestPri agent.Priority
	var bestItem *item
	staleCount := 0

	for i, it := range candidates {
		if it.index < 0 {
			staleCount++
			continue // removed from heap
		}
		t := it.task
		// Check dependencies.
		depsOK := true
		for _, dep := range t.DependsOn {
			if !q.doneIDs[dep] {
				depsOK = false
				break
			}
		}
		if !depsOK {
			continue
		}
		if bestIdx == -1 || t.Priority > bestPri {
			bestIdx = i
			bestPri = t.Priority
			bestItem = it
		}
	}

	// Compact stale entries when >25% of the roleIndex slice is stale.
	// This prevents unbounded growth from repeated push/pop cycles.
	if role != "" && staleCount > 0 && staleCount*4 > len(candidates) {
		q.compactRoleIndex(role)
	}

	if bestItem == nil {
		return nil
	}

	// Remove from the heap.
	heap.Remove(&q.heap, bestItem.index)

	// Remove from role index.
	task := bestItem.task
	q.removeFromRoleIndex(task.AgentRole, bestItem)
	delete(q.inQueue, task.ID)
	return task
}

// compactRoleIndex removes all stale entries (index < 0) from the role's index slice.
// Caller must hold mu.
func (q *Queue) compactRoleIndex(role string) {
	items := q.roleIndex[role]
	n := 0
	for _, it := range items {
		if it.index >= 0 {
			items[n] = it
			n++
		}
	}
	// Clear trailing pointers to allow GC.
	for i := n; i < len(items); i++ {
		items[i] = nil
	}
	q.roleIndex[role] = items[:n]
}

// removeFromRoleIndex removes an item from the role index. Caller must hold mu.
func (q *Queue) removeFromRoleIndex(role string, it *item) {
	items := q.roleIndex[role]
	for i, candidate := range items {
		if candidate == it {
			// Swap with last and truncate — order doesn't matter for the index.
			items[i] = items[len(items)-1]
			q.roleIndex[role] = items[:len(items)-1]
			return
		}
	}
}
