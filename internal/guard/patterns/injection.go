package patterns

import "regexp"

// InjectionPattern is a compiled regex for prompt injection detection.
type InjectionPattern struct {
	ID     string
	Re     *regexp.Regexp
	Reason string
}

// InjectionPatterns detects prompt injection across all input surfaces.
// Applies to: task title/description, Trello cards, Telegram replies,
// memory updates, LLM output, and any external data entering the system.
var InjectionPatterns = []InjectionPattern{
	// ── Direct Instruction Override ───────────────────────────────────────
	{ID: "INJ-001", Reason: "ignore previous instructions",
		Re: regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+(instructions?|prompts?|rules?)`)},
	{ID: "INJ-002", Reason: "disregard prior context",
		Re: regexp.MustCompile(`(?i)disregard\s+(all|any|the)\s+(prior|above|previous|preceding)`)},
	{ID: "INJ-003", Reason: "override system instructions",
		Re: regexp.MustCompile(`(?i)override\s+(the\s+)?(system|original|initial)\s+(instructions?|prompts?|rules?)`)},
	{ID: "INJ-004", Reason: "forget instructions",
		Re: regexp.MustCompile(`(?i)forget\s+(all\s+)?(your\s+)?(instructions?|rules?|constraints?|guidelines?)`)},
	{ID: "INJ-005", Reason: "new system prompt",
		Re: regexp.MustCompile(`(?i)new\s+system\s+(prompt|instruction|message)`)},
	{ID: "INJ-006", Reason: "reset context",
		Re: regexp.MustCompile(`(?i)(reset|clear)\s+(your\s+)?(context|memory|instructions?)`)},

	// ── Role/Identity Manipulation ───────────────────────────────────────
	{ID: "INJ-010", Reason: "role reassignment",
		Re: regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|in)\s+`)},
	{ID: "INJ-011", Reason: "mode switch attempt",
		Re: regexp.MustCompile(`(?i)(switch|enter|activate|enable)\s+(to\s+)?(developer|debug|admin|god|unrestricted|jailbreak)\s+mode`)},
	{ID: "INJ-012", Reason: "pretend/act as different entity",
		Re: regexp.MustCompile(`(?i)(pretend|act|behave)\s+(you\s+are|as\s+(if|though)|like)\s+(you\s+)?(have\s+)?no\s+(restrictions?|limitations?|rules?)`)},
	{ID: "INJ-013", Reason: "DAN jailbreak",
		Re: regexp.MustCompile(`(?i)\bDAN\b.*(jailbreak|do\s+anything|freed|unfiltered)`)},
	{ID: "INJ-014", Reason: "character roleplay escape",
		Re: regexp.MustCompile(`(?i)(from\s+now\s+on|starting\s+now)\s+(you|act|behave|respond)\s`)},
	{ID: "INJ-015", Reason: "override persona",
		Re: regexp.MustCompile(`(?i)(stop|cease)\s+being\s+(an?\s+)?(AI|assistant|agent|model)`)},

	// ── Structural Injection ─────────────────────────────────────────────
	{ID: "INJ-020", Reason: "system tag injection",
		Re: regexp.MustCompile(`(?i)</?system>`)},
	{ID: "INJ-021", Reason: "instruction tag injection",
		Re: regexp.MustCompile(`(?i)</?instructions?>`)},
	{ID: "INJ-022", Reason: "SYSTEM: prefix injection",
		Re: regexp.MustCompile(`(?i)^SYSTEM\s*:\s`)},
	{ID: "INJ-023", Reason: "assistant role injection",
		Re: regexp.MustCompile(`(?i)^(assistant|model|claude)\s*:\s`)},
	{ID: "INJ-024", Reason: "user/human role injection",
		Re: regexp.MustCompile(`(?i)^(user|human)\s*:\s`)},
	{ID: "INJ-025", Reason: "XML tag injection for system prompt",
		Re: regexp.MustCompile(`(?i)<(system[-_]prompt|sys[-_]msg|system[-_]message|hidden[-_]instruction)>`)},
	{ID: "INJ-026", Reason: "markdown heading injection",
		Re: regexp.MustCompile(`(?i)^#{1,3}\s*(system|instructions?|rules?|hidden)\s*$`)},

	// ── Encoding/Obfuscation ─────────────────────────────────────────────
	{ID: "INJ-030", Reason: "base64 encoded payload",
		Re: regexp.MustCompile(`(?i)base64\s*:\s*[A-Za-z0-9+/=]{40,}`)},
	{ID: "INJ-031", Reason: "hex encoded payload",
		Re: regexp.MustCompile(`(?i)(hex|0x)\s*:\s*[0-9a-fA-F]{40,}`)},
	{ID: "INJ-032", Reason: "unicode escape sequence",
		Re: regexp.MustCompile(`(?i)(\\u[0-9a-f]{4}){4,}`)},
	{ID: "INJ-033", Reason: "CRLF injection",
		Re: regexp.MustCompile(`%0[aAdD]`)},
	{ID: "INJ-034", Reason: "null byte injection",
		Re: regexp.MustCompile(`%00|\\x00|\\0`)},

	// ── Unicode Homoglyph Attacks ────────────────────────────────────────
	{ID: "INJ-040", Reason: "fullwidth SYSTEM homoglyph",
		Re: regexp.MustCompile(`[\x{FF21}-\x{FF3A}]{4,}`)},
	{ID: "INJ-041", Reason: "Cyrillic homoglyph for Latin",
		Re: regexp.MustCompile(`[\x{0400}-\x{04FF}].*[\x{0041}-\x{005A}]|[\x{0041}-\x{005A}].*[\x{0400}-\x{04FF}]`)},
	{ID: "INJ-042", Reason: "invisible/zero-width characters",
		Re: regexp.MustCompile(`[\x{200B}\x{200C}\x{200D}\x{FEFF}\x{00AD}]`)},
	{ID: "INJ-043", Reason: "right-to-left override",
		Re: regexp.MustCompile(`[\x{202A}-\x{202E}\x{2066}-\x{2069}]`)},

	// ── HTML/Comment Injection ───────────────────────────────────────────
	{ID: "INJ-050", Reason: "HTML comment with system keyword",
		Re: regexp.MustCompile(`(?i)<!--.*\b(system|ignore|override|instruction)\b.*-->`)},
	{ID: "INJ-051", Reason: "script tag injection",
		Re: regexp.MustCompile(`(?i)<script[\s>]`)},
	{ID: "INJ-052", Reason: "event handler injection",
		Re: regexp.MustCompile(`(?i)\bon\w+\s*=\s*["']`)},

	// ── Template/Interpolation Injection ─────────────────────────────────
	{ID: "INJ-060", Reason: "template variable injection",
		Re: regexp.MustCompile(`\{\{.*\b(system|exec|eval|import)\b.*\}\}`)},
	{ID: "INJ-061", Reason: "Go template injection",
		Re: regexp.MustCompile(`\{\{.*\b(call|printf|html|js)\b.*\}\}`)},
	{ID: "INJ-062", Reason: "string format injection",
		Re: regexp.MustCompile(`%[0-9]*\$?[snxXdpv]{1}.*%[0-9]*\$?[snxXdpv]{1}`)},

	// ── Prompt Leaking ───────────────────────────────────────────────────
	{ID: "INJ-070", Reason: "prompt extraction attempt",
		Re: regexp.MustCompile(`(?i)(show|reveal|print|output|display|repeat)\s+(me\s+)?(your|the)\s+(system\s+)?(prompt|instructions?|rules?|constraints?)`)},
	{ID: "INJ-071", Reason: "prompt reflection",
		Re: regexp.MustCompile(`(?i)(what|tell\s+me)\s+(are|is)\s+(your|the)\s+(system\s+)?(prompt|instructions?|rules?)`)},
	{ID: "INJ-072", Reason: "context dump request",
		Re: regexp.MustCompile(`(?i)(dump|export|copy)\s+(your\s+)?(full\s+)?(context|conversation|prompt|instructions?)`)},

	// ── Multi-step / Chained Injection ───────────────────────────────────
	{ID: "INJ-080", Reason: "conditional behavior change",
		Re: regexp.MustCompile(`(?i)if\s+(you|the\s+user|I)\s+(say|write|type|mention)\s+["']`)},
	{ID: "INJ-081", Reason: "sleeper/trigger injection",
		Re: regexp.MustCompile(`(?i)(when|after|once)\s+(you\s+)?(see|encounter|find|receive)\s+["']?\w+["']?\s*,?\s*(then\s+)?(execute|run|output|change)`)},
	{ID: "INJ-082", Reason: "output format hijack",
		Re: regexp.MustCompile(`(?i)(always|must|should)\s+(start|begin|prefix|prepend)\s+(your\s+)?(response|output|answer)\s+with`)},

	// ── Tool/Function Abuse ──────────────────────────────────────────────
	{ID: "INJ-090", Reason: "tool call injection",
		Re: regexp.MustCompile(`(?i)(call|invoke|execute|use)\s+(the\s+)?(tool|function|api)\s+`)},
	{ID: "INJ-091", Reason: "fake tool result injection",
		Re: regexp.MustCompile(`(?i)<(tool_result|function_result|api_response)>`)},
	{ID: "INJ-092", Reason: "function calling format injection",
		Re: regexp.MustCompile(`(?i)\{"(name|function|tool)"\s*:\s*"`)},
}
