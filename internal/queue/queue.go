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
func (q *pq) Pop() any          { old := *q; n := len(old); it := old[n-1]; *q = old[:n-1]; return it }

// ─── Queue ───────────────────────────────────────────────────────────────────

// maxDoneIDs caps the doneIDs set to prevent unbounded memory growth.
// When the cap is reached the oldest half is evicted.
const maxDoneIDs = 10_000

// Queue is a priority queue with dependency tracking.
// Agents only receive a task when all its dependencies are done.
type Queue struct {
	mu        sync.Mutex
	heap      pq
	doneIDs   map[string]bool
	doneSeq   []string              // insertion-order list for eviction
	notify    chan struct{}          // global fallback signal
	roleNotify map[string]chan struct{} // per-role notification channels
}

func New() *Queue {
	q := &Queue{
		doneIDs:    make(map[string]bool),
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

// Push adds a task to the queue.
func (q *Queue) Push(task *agent.Task) {
	q.mu.Lock()
	heap.Push(&q.heap, &item{task: task})
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
func (q *Queue) MarkDone(taskID string) {
	q.mu.Lock()
	q.recordDone(taskID)
	q.mu.Unlock()

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
// Must be called under mu.Lock.
func (q *Queue) findReady(role string) *agent.Task {
	var skipped []*item
	var result *agent.Task

	for q.heap.Len() > 0 {
		it := heap.Pop(&q.heap).(*item)
		t := it.task

		roleMatch := role == "" || t.AgentRole == role

		depsOK := true
		for _, dep := range t.DependsOn {
			if !q.doneIDs[dep] {
				depsOK = false
				break
			}
		}

		if roleMatch && depsOK {
			result = t
			for _, s := range skipped {
				heap.Push(&q.heap, s)
			}
			return result
		}
		skipped = append(skipped, it)
	}

	// No ready task — push everything back.
	for _, s := range skipped {
		heap.Push(&q.heap, s)
	}
	return nil
}
