package integrations

import (
	"context"

	"github.com/patricksign/AgentClaw/internal/agent"
	"github.com/patricksign/AgentClaw/internal/integrations/telegram"
	"github.com/patricksign/AgentClaw/internal/usecase/escalation"
)

// Compile-time check: ReplyAdapter implements escalation.HumanAsker.
var _ escalation.HumanAsker = (*ReplyAdapter)(nil)

// ReplyAdapter adapts the existing agent.ReplyStore + telegram.DualChannelClient
// to satisfy the escalation.HumanAsker interface.
type ReplyAdapter struct {
	replies  *agent.ReplyStore
	telegram *telegram.DualChannelClient
}

// NewReplyAdapter creates a ReplyAdapter.
func NewReplyAdapter(replies *agent.ReplyStore, tg *telegram.DualChannelClient) *ReplyAdapter {
	return &ReplyAdapter{replies: replies, telegram: tg}
}

// AskHuman sends a question to the human Telegram channel and returns the message ID.
func (a *ReplyAdapter) AskHuman(ctx context.Context, agentID, taskID, taskTitle, questionID, question string) (int, error) {
	if a.telegram == nil || !a.telegram.IsConfigured() {
		return 0, nil
	}
	return a.telegram.AskHuman(ctx, agentID, taskID, taskTitle, question)
}

// RegisterReply registers a message ID for reply tracking and returns a channel
// that will receive the human's answer.
func (a *ReplyAdapter) RegisterReply(msgID int, taskID, questionID string) <-chan string {
	return a.replies.Register(msgID, taskID, questionID)
}
