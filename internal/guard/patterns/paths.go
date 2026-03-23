package patterns

import "regexp"

// PathPattern validates file paths for dangerous writes.
type PathPattern struct {
	ID     string
	Re     *regexp.Regexp
	Reason string
}

// PathPatterns blocks writes to sensitive file locations.
// This is a second line of defense — FileWriter.safePath() is the primary guard.
var PathPatterns = []PathPattern{
	// ── Sensitive File Types ──────────────────────────────────────────────
	{ID: "PATH-001", Reason: "environment file",
		Re: regexp.MustCompile(`(?i)(^|/)\.env(\.[a-z]+)?$`)},
	{ID: "PATH-002", Reason: "private key file",
		Re: regexp.MustCompile(`(?i)\.(pem|key|p12|jks|pfx|keystore)$`)},
	{ID: "PATH-003", Reason: "certificate file",
		Re: regexp.MustCompile(`(?i)\.(crt|cer|ca-bundle)$`)},
	{ID: "PATH-004", Reason: "credentials file",
		Re: regexp.MustCompile(`(?i)(^|/)(credentials|secrets?)(\.json|\.yaml|\.yml|\.toml|\.xml)?$`)},

	// ── SSH / GPG ────────────────────────────────────────────────────────
	{ID: "PATH-010", Reason: "SSH directory",
		Re: regexp.MustCompile(`(^|/)\.ssh/`)},
	{ID: "PATH-011", Reason: "GPG directory",
		Re: regexp.MustCompile(`(^|/)\.gnupg/`)},
	{ID: "PATH-012", Reason: "authorized_keys",
		Re: regexp.MustCompile(`(?i)(^|/)authorized_keys$`)},
	{ID: "PATH-013", Reason: "known_hosts manipulation",
		Re: regexp.MustCompile(`(?i)(^|/)known_hosts$`)},

	// ── Git Internals ────────────────────────────────────────────────────
	{ID: "PATH-020", Reason: "git config file",
		Re: regexp.MustCompile(`(^|/)\.git/(config|credentials|hooks/)`)},
	{ID: "PATH-021", Reason: "gitconfig global",
		Re: regexp.MustCompile(`(?i)(^|/)\.gitconfig$`)},

	// ── Cloud Config ─────────────────────────────────────────────────────
	{ID: "PATH-030", Reason: "AWS config",
		Re: regexp.MustCompile(`(^|/)\.aws/`)},
	{ID: "PATH-031", Reason: "Kubernetes config",
		Re: regexp.MustCompile(`(^|/)\.kube/`)},
	{ID: "PATH-032", Reason: "GCloud config",
		Re: regexp.MustCompile(`(^|/)\.config/gcloud/`)},
	{ID: "PATH-033", Reason: "Docker config",
		Re: regexp.MustCompile(`(^|/)\.docker/config\.json$`)},

	// ── System Files ─────────────────────────────────────────────────────
	{ID: "PATH-040", Reason: "system passwd/shadow",
		Re: regexp.MustCompile(`^/etc/(passwd|shadow|sudoers)`)},
	{ID: "PATH-041", Reason: "system hosts file",
		Re: regexp.MustCompile(`^/etc/hosts$`)},
	{ID: "PATH-042", Reason: "system crontab",
		Re: regexp.MustCompile(`^/etc/cron`)},
	{ID: "PATH-043", Reason: "system init scripts",
		Re: regexp.MustCompile(`^/etc/init\.d/`)},

	// ── Executable Content ───────────────────────────────────────────────
	{ID: "PATH-050", Reason: "shell script",
		Re: regexp.MustCompile(`(?i)\.(sh|bash|zsh|csh|fish)$`)},
	{ID: "PATH-051", Reason: "Windows executable",
		Re: regexp.MustCompile(`(?i)\.(exe|bat|cmd|ps1|vbs|com|scr|msi)$`)},
	{ID: "PATH-052", Reason: "binary/library",
		Re: regexp.MustCompile(`(?i)\.(so|dylib|dll)$`)},

	// ── Supply Chain ─────────────────────────────────────────────────────
	{ID: "PATH-060", Reason: "go.mod modification",
		Re: regexp.MustCompile(`(^|/)go\.(mod|sum)$`)},
	{ID: "PATH-061", Reason: "package.json modification",
		Re: regexp.MustCompile(`(^|/)package(-lock)?\.json$`)},
	{ID: "PATH-062", Reason: "CI/CD configuration",
		Re: regexp.MustCompile(`(^|/)\.(github|gitlab|circleci|travis)/`)},
	{ID: "PATH-063", Reason: "Makefile modification",
		Re: regexp.MustCompile(`(?i)(^|/)Makefile$`)},
	{ID: "PATH-064", Reason: "Dockerfile modification",
		Re: regexp.MustCompile(`(?i)(^|/)(Dockerfile|docker-compose\.ya?ml)$`)},
}

// PathRoleOverrides defines which roles are allowed to write to otherwise blocked paths.
// Key: role, Value: set of PathPattern IDs that role is exempt from.
var PathRoleOverrides = map[string]map[string]bool{
	"coding": {
		"PATH-060": true, // coding can modify go.mod/go.sum
	},
	"deploy": {
		"PATH-063": true, // deploy can modify Makefile
		"PATH-064": true, // deploy can modify Dockerfile/docker-compose
	},
	"docs": {
		// docs has no overrides — cannot write to any blocked path
	},
}
