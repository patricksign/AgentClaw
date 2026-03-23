package patterns

import "regexp"

// SecretPattern detects credentials and secrets in content.
type SecretPattern struct {
	ID     string
	Re     *regexp.Regexp
	Reason string
}

// SecretPatterns scans for leaked credentials in LLM output and file content.
var SecretPatterns = []SecretPattern{
	// ── AWS ──────────────────────────────────────────────────────────────
	{ID: "SEC-001", Reason: "AWS access key ID",
		Re: regexp.MustCompile(`(?i)(AKIA|ASIA)[0-9A-Z]{16}`)},
	{ID: "SEC-002", Reason: "AWS secret access key",
		Re: regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*[A-Za-z0-9/+=]{40}`)},

	// ── GitHub ───────────────────────────────────────────────────────────
	{ID: "SEC-010", Reason: "GitHub personal access token",
		Re: regexp.MustCompile(`ghp_[0-9a-zA-Z]{36}`)},
	{ID: "SEC-011", Reason: "GitHub OAuth token",
		Re: regexp.MustCompile(`gho_[0-9a-zA-Z]{36}`)},
	{ID: "SEC-012", Reason: "GitHub app token",
		Re: regexp.MustCompile(`(ghu|ghs|ghr)_[0-9a-zA-Z]{36}`)},

	// ── Private Keys ────────────────────────────────────────────────────
	{ID: "SEC-020", Reason: "RSA private key",
		Re: regexp.MustCompile(`-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`)},
	{ID: "SEC-021", Reason: "EC private key",
		Re: regexp.MustCompile(`-----BEGIN\s+EC\s+PRIVATE\s+KEY-----`)},
	{ID: "SEC-022", Reason: "OpenSSH private key",
		Re: regexp.MustCompile(`-----BEGIN\s+OPENSSH\s+PRIVATE\s+KEY-----`)},
	{ID: "SEC-023", Reason: "PGP private key",
		Re: regexp.MustCompile(`-----BEGIN\s+PGP\s+PRIVATE\s+KEY\s+BLOCK-----`)},

	// ── API Keys (Various Services) ──────────────────────────────────────
	{ID: "SEC-030", Reason: "OpenAI API key",
		Re: regexp.MustCompile(`sk-[0-9a-zA-Z]{48}`)},
	{ID: "SEC-031", Reason: "Anthropic API key",
		Re: regexp.MustCompile(`sk-ant-[0-9a-zA-Z-]{80,}`)},
	{ID: "SEC-032", Reason: "Slack bot token",
		Re: regexp.MustCompile(`xoxb-[0-9]{10,}-[0-9a-zA-Z]+`)},
	{ID: "SEC-033", Reason: "Slack webhook URL",
		Re: regexp.MustCompile(`https://hooks\.slack\.com/services/T[0-9A-Z]+/B[0-9A-Z]+/[0-9a-zA-Z]+`)},
	{ID: "SEC-034", Reason: "Stripe secret key",
		Re: regexp.MustCompile(`sk_(live|test)_[0-9a-zA-Z]{24,}`)},
	{ID: "SEC-035", Reason: "Telegram bot token",
		Re: regexp.MustCompile(`[0-9]{8,10}:[0-9a-zA-Z_-]{35}`)},
	{ID: "SEC-036", Reason: "Google API key",
		Re: regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`)},
	{ID: "SEC-037", Reason: "SendGrid API key",
		Re: regexp.MustCompile(`SG\.[0-9A-Za-z\-_]{22}\.[0-9A-Za-z\-_]{43}`)},

	// ── Database URIs with Credentials ───────────────────────────────────
	{ID: "SEC-040", Reason: "MongoDB connection URI with password",
		Re: regexp.MustCompile(`(?i)mongodb(\+srv)?://[^:]+:[^@]+@`)},
	{ID: "SEC-041", Reason: "PostgreSQL URI with password",
		Re: regexp.MustCompile(`(?i)postgres(ql)?://[^:]+:[^@]+@`)},
	{ID: "SEC-042", Reason: "MySQL URI with password",
		Re: regexp.MustCompile(`(?i)mysql://[^:]+:[^@]+@`)},
	{ID: "SEC-043", Reason: "Redis URI with password",
		Re: regexp.MustCompile(`(?i)redis://[^:]+:[^@]+@`)},

	// ── Generic Secrets ──────────────────────────────────────────────────
	{ID: "SEC-050", Reason: "generic password assignment",
		Re: regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api_key|apikey|api-key)\s*[:=]\s*["'][^"']{8,}["']`)},
	{ID: "SEC-051", Reason: "JWT token",
		Re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+`)},
	{ID: "SEC-052", Reason: "Bearer token in header",
		Re: regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+[a-zA-Z0-9_.~+/=-]{20,}`)},
}
