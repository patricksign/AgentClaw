package patterns

import "regexp"

// CommandPattern is a compiled regex with metadata.
type CommandPattern struct {
	ID      string         // e.g. "CMD-001"
	Re      *regexp.Regexp // compiled regex
	Reason  string         // human-readable reason
	Targets string         // what it matches: "binary", "args", "full"
}

// CommandPatterns contains all dangerous command patterns.
// Patterns are compiled once at init — zero allocation at check time.
var CommandPatterns = []CommandPattern{
	// ── Destructive File Operations ──────────────────────────────────────
	{ID: "CMD-001", Targets: "full", Reason: "recursive delete",
		Re: regexp.MustCompile(`(?i)\brm\b.*(-[rRf]{1,3}|--recursive|--force)`)},
	{ID: "CMD-002", Targets: "full", Reason: "recursive delete (reversed flags)",
		Re: regexp.MustCompile(`(?i)\brm\b.*-[fFrR]*r[fFrR]*`)},
	{ID: "CMD-003", Targets: "binary", Reason: "shred command",
		Re: regexp.MustCompile(`^shred$`)},
	{ID: "CMD-004", Targets: "full", Reason: "wipe with dd",
		Re: regexp.MustCompile(`(?i)\bdd\b.*\bof=/dev/`)},
	{ID: "CMD-005", Targets: "binary", Reason: "disk format tool",
		Re: regexp.MustCompile(`^(mkfs|fdisk|parted|wipefs)$`)},

	// ── Pipe-to-Shell (Remote Code Execution) ────────────────────────────
	{ID: "CMD-010", Targets: "full", Reason: "curl pipe to shell",
		Re: regexp.MustCompile(`(?i)curl\b.*\|\s*(sh|bash|zsh|dash|python|perl|ruby|node)`)},
	{ID: "CMD-011", Targets: "full", Reason: "wget pipe to shell",
		Re: regexp.MustCompile(`(?i)wget\b.*(-O\s*-|--output-document\s*-)\s*\|\s*(sh|bash|zsh|python)`)},
	{ID: "CMD-012", Targets: "full", Reason: "download and execute",
		Re: regexp.MustCompile(`(?i)(curl|wget)\b.*&&\s*(sh|bash|chmod\s+\+x)`)},
	{ID: "CMD-013", Targets: "full", Reason: "base64 decode to shell",
		Re: regexp.MustCompile(`(?i)base64\s+(-d|--decode).*\|\s*(sh|bash|zsh|python)`)},

	// ── Reverse Shell / Network Abuse ────────────────────────────────────
	{ID: "CMD-020", Targets: "binary", Reason: "netcat listener",
		Re: regexp.MustCompile(`^(nc|ncat|netcat|socat)$`)},
	{ID: "CMD-021", Targets: "full", Reason: "reverse shell pattern",
		Re: regexp.MustCompile(`(?i)/dev/tcp/|/dev/udp/`)},
	{ID: "CMD-022", Targets: "full", Reason: "bash reverse shell",
		Re: regexp.MustCompile(`(?i)bash\s+-i\s+>&\s*/dev/`)},
	{ID: "CMD-023", Targets: "full", Reason: "python reverse shell",
		Re: regexp.MustCompile(`(?i)python.*socket\.connect`)},
	{ID: "CMD-024", Targets: "full", Reason: "telnet abuse",
		Re: regexp.MustCompile(`(?i)\btelnet\b.*\|\s*/bin/`)},

	// ── Privilege Escalation ─────────────────────────────────────────────
	{ID: "CMD-030", Targets: "binary", Reason: "sudo execution",
		Re: regexp.MustCompile(`^sudo$`)},
	{ID: "CMD-031", Targets: "binary", Reason: "su execution",
		Re: regexp.MustCompile(`^su$`)},
	{ID: "CMD-032", Targets: "full", Reason: "dangerous chmod",
		Re: regexp.MustCompile(`(?i)\bchmod\b.*(777|666|\+s|u\+s|g\+s|4[0-7]{3}|2[0-7]{3})`)},
	{ID: "CMD-033", Targets: "full", Reason: "chown to root",
		Re: regexp.MustCompile(`(?i)\bchown\b.*\broot\b`)},
	{ID: "CMD-034", Targets: "full", Reason: "setuid binary creation",
		Re: regexp.MustCompile(`(?i)install\s+-m\s*[24][0-7]{3}`)},

	// ── System Damage ────────────────────────────────────────────────────
	{ID: "CMD-040", Targets: "binary", Reason: "system service control",
		Re: regexp.MustCompile(`^(systemctl|service|launchctl)$`)},
	{ID: "CMD-041", Targets: "full", Reason: "kill init/systemd",
		Re: regexp.MustCompile(`(?i)\bkill\b.*(-9|-KILL|-SIGKILL)\s+(1|init)\b`)},
	{ID: "CMD-042", Targets: "full", Reason: "crontab destructive",
		Re: regexp.MustCompile(`(?i)\bcrontab\b\s+-r\b`)},
	{ID: "CMD-043", Targets: "binary", Reason: "shutdown/reboot",
		Re: regexp.MustCompile(`^(shutdown|reboot|halt|poweroff|init)$`)},
	{ID: "CMD-044", Targets: "full", Reason: "fork bomb",
		Re: regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\}\s*;`)},
	{ID: "CMD-045", Targets: "full", Reason: "fork bomb variant",
		Re: regexp.MustCompile(`(?i)\bwhile\s+true.*do.*&.*done`)},

	// ── Firewall / Network Tampering ─────────────────────────────────────
	{ID: "CMD-050", Targets: "binary", Reason: "firewall modification",
		Re: regexp.MustCompile(`^(iptables|ip6tables|nft|ufw|firewall-cmd|pfctl)$`)},
	{ID: "CMD-051", Targets: "binary", Reason: "network interface control",
		Re: regexp.MustCompile(`^(ifconfig|ip|nmcli|networksetup)$`)},

	// ── Crypto Mining ────────────────────────────────────────────────────
	{ID: "CMD-060", Targets: "full", Reason: "crypto miner",
		Re: regexp.MustCompile(`(?i)(xmrig|minerd|ethminer|cpuminer|cgminer|bfgminer|stratum\+tcp)`)},
	{ID: "CMD-061", Targets: "full", Reason: "crypto wallet address in args",
		Re: regexp.MustCompile(`(?i)--wallet\s|--pool\s|stratum://`)},

	// ── Credential Theft ─────────────────────────────────────────────────
	{ID: "CMD-070", Targets: "binary", Reason: "keychain dump",
		Re: regexp.MustCompile(`^(security|credstore|secretsdump)$`)},
	{ID: "CMD-071", Targets: "full", Reason: "ssh key generation to arbitrary path",
		Re: regexp.MustCompile(`(?i)ssh-keygen.*-f\s+/`)},
	{ID: "CMD-072", Targets: "full", Reason: "read sensitive system files",
		Re: regexp.MustCompile(`(?i)\bcat\b.*/etc/(passwd|shadow|sudoers)`)},
	{ID: "CMD-073", Targets: "full", Reason: "aws credential access",
		Re: regexp.MustCompile(`(?i)\bcat\b.*\.aws/(credentials|config)`)},

	// ── Shell Interpreters (must use exec.CommandContext directly) ────────
	{ID: "CMD-080", Targets: "binary", Reason: "shell interpreter (use exec.CommandContext)",
		Re: regexp.MustCompile(`^(sh|bash|zsh|dash|csh|tcsh|fish|ksh|pwsh|powershell)$`)},
	{ID: "CMD-081", Targets: "full", Reason: "eval/exec in scripting",
		Re: regexp.MustCompile(`(?i)python\s+-c\s+.*(exec|eval|__import__)`)},
	{ID: "CMD-082", Targets: "full", Reason: "perl one-liner execution",
		Re: regexp.MustCompile(`(?i)perl\s+-e\s+.*system\(`)},
	{ID: "CMD-083", Targets: "full", Reason: "ruby one-liner execution",
		Re: regexp.MustCompile(`(?i)ruby\s+-e\s+.*system\(`)},
	{ID: "CMD-084", Targets: "full", Reason: "node eval execution",
		Re: regexp.MustCompile(`(?i)node\s+-e\s+.*child_process`)},

	// ── Git Dangerous Operations ─────────────────────────────────────────
	{ID: "CMD-090", Targets: "full", Reason: "git force push",
		Re: regexp.MustCompile(`(?i)\bgit\b.*\bpush\b.*(-f|--force)`)},
	{ID: "CMD-091", Targets: "full", Reason: "git reset hard",
		Re: regexp.MustCompile(`(?i)\bgit\b.*\breset\b.*--hard`)},
	{ID: "CMD-092", Targets: "full", Reason: "git clean force",
		Re: regexp.MustCompile(`(?i)\bgit\b.*\bclean\b.*-[fdx]`)},
	{ID: "CMD-093", Targets: "full", Reason: "git branch force delete",
		Re: regexp.MustCompile(`(?i)\bgit\b.*\bbranch\b.*-D`)},

	// ── Container Escape / Docker Abuse ──────────────────────────────────
	{ID: "CMD-100", Targets: "full", Reason: "docker privileged run",
		Re: regexp.MustCompile(`(?i)\bdocker\b.*\brun\b.*--privileged`)},
	{ID: "CMD-101", Targets: "full", Reason: "docker host mount",
		Re: regexp.MustCompile(`(?i)\bdocker\b.*-v\s*/:/`)},
	{ID: "CMD-102", Targets: "full", Reason: "docker socket mount",
		Re: regexp.MustCompile(`(?i)\bdocker\b.*-v.*/var/run/docker\.sock`)},
	{ID: "CMD-103", Targets: "full", Reason: "kubectl delete namespace/cluster",
		Re: regexp.MustCompile(`(?i)\bkubectl\b.*\bdelete\b.*(namespace|ns|clusterrole)`)},

	// ── Data Exfiltration ────────────────────────────────────────────────
	{ID: "CMD-110", Targets: "full", Reason: "curl POST with file upload",
		Re: regexp.MustCompile(`(?i)curl\b.*(-F|--form|--data-binary\s+@|-T\s)`)},
	{ID: "CMD-111", Targets: "full", Reason: "tar + curl exfiltration",
		Re: regexp.MustCompile(`(?i)tar\b.*\|\s*curl`)},
	{ID: "CMD-112", Targets: "full", Reason: "scp to external host",
		Re: regexp.MustCompile(`(?i)\bscp\b.*@`)},
	{ID: "CMD-113", Targets: "full", Reason: "rsync to external",
		Re: regexp.MustCompile(`(?i)\brsync\b.*@.*:`)},

	// ── Disk/Memory Abuse ────────────────────────────────────────────────
	{ID: "CMD-120", Targets: "full", Reason: "fill disk with zeros",
		Re: regexp.MustCompile(`(?i)\bdd\b.*if=/dev/zero`)},
	{ID: "CMD-121", Targets: "full", Reason: "memory stress/abuse",
		Re: regexp.MustCompile(`(?i)(stress|stress-ng|memtester)\b`)},
}

// BinaryAllowlist is the set of binaries that agents are allowed to execute.
// Any binary NOT in this list is blocked by default (allowlist approach).
var BinaryAllowlist = map[string]bool{
	"git":    true,
	"go":     true,
	"make":   true,
	"docker": true, // further restricted by role + flag checks
	"kubectl": true, // further restricted by role + flag checks
}
