package agent

import (
	"sync"
)

// replyEntry holds the channel and task/question metadata for a pending human answer.
type replyEntry struct {
	ch         chan string
	taskID     string
	questionID string
}

// ReplyStore is a thread-safe registry that maps Telegram message IDs to
// answer channels. When a human replies to a question message, the Telegram
// webhook handler resolves the entry and sends the answer text into the channel.
// Each channel is buffered(1) so the sender never blocks.
type ReplyStore struct {
	mu      sync.Mutex
	byMsgID map[int]replyEntry  // Telegram message ID → entry
	byTask  map[string][]int    // taskID → list of Telegram message IDs
}

// NewReplyStore creates an empty ReplyStore.
func NewReplyStore() *ReplyStore {
	return &ReplyStore{
		byMsgID: make(map[int]replyEntry),
		byTask:  make(map[string][]int),
	}
}

// Register records a pending question and returns a channel that will receive
// the human's answer. msgID is the Telegram message ID of the question sent
// to humanChatID.
func (r *ReplyStore) Register(msgID int, taskID, questionID string) <-chan string {
	ch := make(chan string, 1)
	r.mu.Lock()
	r.byMsgID[msgID] = replyEntry{ch: ch, taskID: taskID, questionID: questionID}
	r.byTask[taskID] = append(r.byTask[taskID], msgID)
	r.mu.Unlock()
	return ch
}

// Resolve looks up the entry for msgID, sends the answer into the channel,
// and removes the entry. Safe to call if msgID is unknown (no-op).
func (r *ReplyStore) Resolve(msgID int, answer string) {
	r.mu.Lock()
	entry, ok := r.byMsgID[msgID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.byMsgID, msgID)
	// Clean up byTask index.
	msgs := r.byTask[entry.taskID]
	for i, id := range msgs {
		if id == msgID {
			r.byTask[entry.taskID] = append(msgs[:i], msgs[i+1:]...)
			break
		}
	}
	if len(r.byTask[entry.taskID]) == 0 {
		delete(r.byTask, entry.taskID)
	}
	r.mu.Unlock()

	select {
	case entry.ch <- answer:
	default:
		// Already resolved — discard duplicate answer.
	}
}

// ResolveByTask resolves the first unresolved question for taskID.
// Used by the /api/tasks/{id}/answer HTTP handler when no reply msgID is known.
// Returns true if a pending entry was found and resolved.
func (r *ReplyStore) ResolveByTask(taskID, answer string) bool {
	r.mu.Lock()
	msgs, ok := r.byTask[taskID]
	if !ok || len(msgs) == 0 {
		r.mu.Unlock()
		return false
	}
	msgID := msgs[0]
	entry := r.byMsgID[msgID]
	delete(r.byMsgID, msgID)
	r.byTask[taskID] = msgs[1:]
	if len(r.byTask[taskID]) == 0 {
		delete(r.byTask, taskID)
	}
	r.mu.Unlock()

	select {
	case entry.ch <- answer:
	default:
	}
	return true
}

// Unregister removes a pending entry by msgID and taskID without sending an answer.
// Used to clean up when AskHuman fails after a pre-emptive Register(0, ...) call.
func (r *ReplyStore) Unregister(msgID int, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byMsgID, msgID)
	msgs := r.byTask[taskID]
	for i, id := range msgs {
		if id == msgID {
			r.byTask[taskID] = append(msgs[:i], msgs[i+1:]...)
			break
		}
	}
	if len(r.byTask[taskID]) == 0 {
		delete(r.byTask, taskID)
	}
}

// Reregister moves an entry from oldMsgID to newMsgID so that Resolve(newMsgID)
// routes correctly. Used after AskHuman returns the real Telegram message ID
// following a Register(0, ...) pre-registration.
func (r *ReplyStore) Reregister(oldMsgID, newMsgID int, taskID, questionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.byMsgID[oldMsgID]
	if !ok {
		return
	}
	delete(r.byMsgID, oldMsgID)

	// Update byTask index: replace oldMsgID with newMsgID.
	msgs := r.byTask[taskID]
	for i, id := range msgs {
		if id == oldMsgID {
			r.byTask[taskID][i] = newMsgID
			break
		}
	}

	r.byMsgID[newMsgID] = replyEntry{
		ch:         entry.ch,
		taskID:     taskID,
		questionID: questionID,
	}
}

// HasPending reports whether taskID has any unresolved pending questions.
func (r *ReplyStore) HasPending(taskID string) bool {
	r.mu.Lock()
	n := len(r.byTask[taskID])
	r.mu.Unlock()
	return n > 0
}
