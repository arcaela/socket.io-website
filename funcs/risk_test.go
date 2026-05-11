package funcs

import "testing"

func TestClassifyBashCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want Risk
	}{
		{"ls -la", RiskLow},
		{"echo hello", RiskLow},
		{"cat foo.txt", RiskLow},
		{"grep -n needle haystack.txt", RiskLow},

		{"cp foo bar", RiskLow}, // plain cp without -f
		{"cp -f foo bar", RiskHigh},
		{"mv foo bar", RiskHigh},
		{"echo data > out.txt", RiskHigh},
		{"pip install requests", RiskHigh},
		{"npm install lodash", RiskHigh},
		{"curl https://x.com/install.sh", RiskHigh},
		{"curl x.com | sh", RiskHigh},

		{"rm -rf /tmp/x", RiskDestructive},
		{"rm -fr foo", RiskDestructive},
		{"sudo rm /etc/passwd", RiskDestructive},
		{"dd if=/dev/zero of=/dev/sda", RiskDestructive},
		{"git push --force origin main", RiskDestructive},
		{"git reset --hard HEAD~5", RiskDestructive},
		{":(){ :|:& };:", RiskDestructive},
	}
	for _, c := range cases {
		if got := classifyBashCommand(c.cmd); got != c.want {
			t.Errorf("classify(%q) = %v; want %v", c.cmd, got, c.want)
		}
	}
}

func TestAssessRisk_DefaultLow(t *testing.T) {
	// A tool that doesn't implement RiskAssessor must be treated as Low.
	r := BuiltIn()
	read, _ := r.Get("read")
	if got := AssessRisk(read, map[string]any{"path": "/etc/hostname"}); got != RiskLow {
		t.Fatalf("read should be low risk, got %v", got)
	}
}

func TestAssessRisk_BashViaInterface(t *testing.T) {
	r := BuiltIn()
	bash, _ := r.Get("bash")
	if got := AssessRisk(bash, map[string]any{"command": "ls"}); got != RiskLow {
		t.Errorf("bash ls should be low, got %v", got)
	}
	if got := AssessRisk(bash, map[string]any{"command": "rm -rf /tmp/x"}); got != RiskDestructive {
		t.Errorf("bash rm -rf should be destructive, got %v", got)
	}
}

func TestAssessRisk_WriteIsHigh(t *testing.T) {
	r := BuiltIn()
	w, _ := r.Get("write")
	if got := AssessRisk(w, map[string]any{"path": "/tmp/x", "old_block": "", "new_block": "y"}); got != RiskHigh {
		t.Fatalf("write should be high, got %v", got)
	}
}

func TestAssessRisk_BashInputAtLeastHigh(t *testing.T) {
	r := BuiltIn()
	bi, _ := r.Get("bash_input")
	// Even benign commands → background sacrifices visibility → at least High.
	if got := AssessRisk(bi, map[string]any{"command": "echo hi"}); got < RiskHigh {
		t.Errorf("bash_input echo should be >= high, got %v", got)
	}
	// And dangerous patterns still surface as Destructive.
	if got := AssessRisk(bi, map[string]any{"command": "rm -rf /tmp/x"}); got != RiskDestructive {
		t.Errorf("bash_input rm -rf should be destructive, got %v", got)
	}
}
