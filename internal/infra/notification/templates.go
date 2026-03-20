package notification

import (
	"fmt"
	"strings"

	"github.com/patricksign/agentclaw/internal/domain"
)

// Format returns the HTML-formatted notification text and the target channel
// for a given domain event.
func Format(event domain.Event) (text string, channel domain.Channel) {
	taskID := event.TaskID
	agentID := event.AgentID
	role := event.AgentRole
	model := event.Model
	msg := event.Payload["message"]

	switch event.Type {
	case domain.EventTaskStarted:
		text = fmt.Sprintf(
			"🚀 <b>Task Started</b>\nAgent: <code>%s</code> (%s)\nTask: <code>%s</code>\nModel: <code>%s</code>",
			esc(agentID), esc(role), esc(taskID), esc(model))
		channel = domain.StatusChannel

	case domain.EventTaskDone:
		tokens := event.Payload["input_tokens"] + " in / " + event.Payload["output_tokens"] + " out"
		cost := event.Payload["cost_usd"]
		dur := event.Payload["duration_ms"]
		text = fmt.Sprintf(
			"✅ <b>Task Done</b>\nAgent: <code>%s</code>\nTask: <code>%s</code>\nTokens: %s | Cost: $%s | Time: %sms",
			esc(agentID), esc(taskID), tokens, cost, dur)
		channel = domain.StatusChannel

	case domain.EventTaskFailed:
		text = fmt.Sprintf(
			"❌ <b>Task Failed</b>\nAgent: <code>%s</code>\nTask: <code>%s</code>\nReason: %s",
			esc(agentID), esc(taskID), esc(msg))
		channel = domain.StatusChannel

	case domain.EventPhaseTransition:
		text = fmt.Sprintf(
			"🔄 <b>Phase</b>\nTask: <code>%s</code>\n%s",
			esc(taskID), esc(msg))
		channel = domain.StatusChannel

	case domain.EventQuestionAsked:
		text = fmt.Sprintf(
			"🤔 <b>Question — needs human input</b>\nTask: <code>%s</code>\n%s",
			esc(taskID), esc(msg))
		channel = domain.StatusChannel

	case domain.EventQuestionAnswered:
		answeredBy := event.Payload["answered_by"]
		text = fmt.Sprintf(
			"🤖 <b>Question resolved by %s</b>\nTask: <code>%s</code>\n%s",
			esc(answeredBy), esc(taskID), esc(msg))
		channel = domain.StatusChannel

	case domain.EventQuestionExpired:
		text = fmt.Sprintf(
			"⚠️ <b>Question expired</b>\nTask: <code>%s</code>\n%s",
			esc(taskID), esc(msg))
		channel = domain.HumanChannel

	case domain.EventEscalated:
		text = fmt.Sprintf(
			"⬆️ <b>Escalated</b>\nTask: <code>%s</code>\n%s",
			esc(taskID), esc(msg))
		channel = event.Channel // use the channel from the event itself

	case domain.EventPlanSubmitted:
		text = fmt.Sprintf(
			"📋 <b>Plan submitted for Opus review</b>\nAgent: <code>%s</code>\nTask: <code>%s</code>",
			esc(agentID), esc(taskID))
		channel = domain.StatusChannel

	case domain.EventPlanApproved:
		plan := event.Payload["plan"]
		text = fmt.Sprintf(
			"✅ <b>Plan Approved by Opus</b>\nAgent: <code>%s</code>\nTask: <code>%s</code>\nPlan: %s\n\nImplementation will begin now.",
			esc(agentID), esc(taskID), esc(plan))
		channel = domain.HumanChannel

	case domain.EventPlanRedirected:
		guidance := event.Payload["guidance"]
		attempt := event.Payload["attempt"]
		text = fmt.Sprintf(
			"🔄 <b>Plan Redirected by Opus</b>\nAgent: <code>%s</code>\nTask: <code>%s</code>\nGuidance: %s\nAttempt: %s",
			esc(agentID), esc(taskID), esc(guidance), attempt)
		channel = domain.HumanChannel

	case domain.EventPlanFailed:
		text = fmt.Sprintf(
			"🚨 <b>Plan failed — manual intervention needed</b>\nTask: <code>%s</code>\n%s",
			esc(taskID), esc(msg))
		channel = domain.HumanChannel

	case domain.EventParallelStarted:
		text = fmt.Sprintf("⚡ <b>Parallel execution</b>\n%s", esc(msg))
		channel = domain.StatusChannel

	case domain.EventParallelDone:
		text = fmt.Sprintf("✅ <b>Parallel done</b>\n%s", esc(msg))
		channel = domain.StatusChannel

	default:
		text = fmt.Sprintf("<b>%s</b>\nTask: <code>%s</code>\n%s", event.Type, esc(taskID), esc(msg))
		channel = domain.StatusChannel
	}

	return text, channel
}

// esc escapes HTML special characters for Telegram HTML parse mode.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
