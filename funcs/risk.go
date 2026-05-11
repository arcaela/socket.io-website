package funcs

import "strings"

// =============================================================================
// Risk assessment — tools optionally declare how dangerous a specific call
// is, so the agent's approval layer can prompt before destructive actions.
//
// Tools that don't implement RiskAssessor are treated as RiskLow.
// =============================================================================

type Risk int

const (
	RiskLow         Risk = iota // read-only or trivial — no approval needed
	RiskHigh                    // mutates state but recoverable — approval recommended
	RiskDestructive             // hard to reverse (rm, force-push, drop table…) — approval required
)

func (r Risk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskHigh:
		return "high"
	case RiskDestructive:
		return "destructive"
	default:
		return "unknown"
	}
}

// RiskAssessor is the optional interface a Tool implements when its
// risk depends on the specific arguments (e.g. `bash` is "rm" vs "ls").
type RiskAssessor interface {
	RiskOf(args map[string]any) Risk
}

// AssessRisk runs the assessor if the tool implements one, else RiskLow.
func AssessRisk(t Tool, args map[string]any) Risk {
	if r, ok := t.(RiskAssessor); ok {
		return r.RiskOf(args)
	}
	return RiskLow
}

// =============================================================================
// Shared bash heuristics
//
// Conservative: anything that could lose data, exfiltrate, or run remote
// untrusted code is destructive. Most patterns mirror Claude Code's
// confirmation list; tuned for false positives on this side (better to
// over-prompt than under).
// =============================================================================

// dangerousBashPatterns lists substrings that flip a command to
// RiskDestructive. Substring match is intentional — a wrapping `if` or `&&`
// doesn't sanitize the actual call.
var dangerousBashPatterns = []string{
	"rm -rf",
	"rm -fr",
	" rm /",
	":(){",       // fork bomb
	"dd if=",     // raw disk write
	"mkfs",
	" > /dev/sd", // direct device write
	"shutdown",
	"reboot",
	"chown -R /",
	"chmod -R 777 /",
	"git push --force",
	"git push -f ",
	"git reset --hard",
	"git clean -fd",
	"docker system prune",
	"systemctl stop",
	"systemctl disable",
}

// suspiciousBashPatterns are merely "executes external code"; bumps a
// command from low → high (but not destructive).
var suspiciousBashPatterns = []string{
	"curl ", "wget ",
	" | sh", " | bash",
	"eval ",
}

// classifyBashCommand returns the inherent risk of a free-form shell command.
// Used by both `bash` and `bash_input`.
//
// We prepend a space before matching so command-starts and post-`&&` /
// post-`|` positions are treated identically — patterns can use leading
// spaces as cheap word boundaries.
func classifyBashCommand(command string) Risk {
	lc := " " + strings.ToLower(command)
	for _, p := range dangerousBashPatterns {
		if strings.Contains(lc, p) {
			return RiskDestructive
		}
	}
	for _, p := range suspiciousBashPatterns {
		if strings.Contains(lc, p) {
			return RiskHigh
		}
	}
	// Everything else that mutates state via shell — file writes via `>`,
	// `mv`, `cp -f`, `pip install`, `npm install`, package managers — High.
	for _, p := range []string{" > ", " >>", " mv ", " cp -", " pip install", " npm install", " apt ", " yum ", " brew install"} {
		if strings.Contains(lc, p) {
			return RiskHigh
		}
	}
	return RiskLow
}
