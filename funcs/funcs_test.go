package funcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- Registry ----------

func TestRegistry_RegisterAndDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(bashTool{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(bashTool{}); err == nil {
		t.Fatal("expected duplicate registration to error")
	}
	if !r.Has("bash") {
		t.Fatal("registry should have bash after registration")
	}
}

func TestRegistry_BuiltInValidates(t *testing.T) {
	r := BuiltIn()
	if err := r.Validate(); err != nil {
		t.Fatalf("BuiltIn registry failed validation: %v", err)
	}
	expected := []string{"bash", "bash_input", "bash_kill", "bash_output", "glob", "memory", "read", "task", "web_fetch", "web_search", "write"}
	got := []string{}
	for _, tl := range r.List() {
		got = append(got, tl.Name())
	}
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("BuiltIn tools mismatch:\n got: %v\nwant: %v", got, expected)
	}
}

func TestRegistry_KindsAreCorrect(t *testing.T) {
	r := BuiltIn()
	wantKind := map[string]Kind{
		"bash":   KindBase,
		"write":  KindBase,
		"read":   KindComposite,
		"glob":   KindComposite,
		"task":   KindComposite,
		"memory": KindComposite,
	}
	for name, want := range wantKind {
		tl, ok := r.Get(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if tl.Kind() != want {
			t.Errorf("kind(%s) = %v; want %v", name, tl.Kind(), want)
		}
	}
}

func TestRegistry_UnknownTool(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error calling unknown tool")
	}
}

// ---------- BashTool (base) ----------

func TestBash_Echo(t *testing.T) {
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash", map[string]any{
		"command":     `echo "hello world"`,
		"description": "say hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashResult)
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello world") {
		t.Fatalf("bad result: %+v", res)
	}
	if res.Description != "say hello" {
		t.Fatalf("description not threaded: %+v", res)
	}
}

func TestBash_RequiresDescription(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "bash", map[string]any{
		"command": "true",
	})
	if err == nil {
		t.Fatal("expected error: description is now required")
	}
}

func TestBash_RequiresCommand(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "bash", map[string]any{
		"description": "x",
	})
	if err == nil {
		t.Fatal("expected error: command is required")
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash", map[string]any{
		"command":     "exit 7",
		"description": "fail intentionally",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.(BashResult).ExitCode != 7 {
		t.Fatalf("expected exit 7, got %+v", out)
	}
}

func TestBash_ContextCancellation(t *testing.T) {
	r := BuiltIn()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, _ = r.Call(ctx, "bash", map[string]any{
		"command":     "sleep 5",
		"description": "long sleep",
	})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("bash did not honor cancel")
	}
}


func TestBash_TimeoutZeroMeansNoTimeout(t *testing.T) {
	r := BuiltIn()
	// 0 = no timeout. Run a command that takes longer than the default 120s
	// would be a slow test, so we just verify it does NOT timed out and the
	// real exit_code is reported (i.e. we do not enter the deadline branch).
	out, err := r.Call(context.Background(), "bash", map[string]any{
		"command":         "sleep 0.2 && echo ok",
		"description":     "no-timeout sentinel",
		"timeout_seconds": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashResult)
	if res.TimedOut {
		t.Fatalf("timeout=0 should never time out, got %+v", res)
	}
	if !strings.Contains(res.Stdout, "ok") {
		t.Fatalf("expected ok in stdout, got %q", res.Stdout)
	}
}

func TestBash_TimeoutHonored(t *testing.T) {
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash", map[string]any{
		"command":         "sleep 5",
		"description":     "should timeout",
		"timeout_seconds": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashResult)
	if !res.TimedOut {
		t.Fatalf("expected TimedOut=true, got %+v", res)
	}
}

// ---------- BashTool background mode ----------




// ---------- WriteTool (base) ----------

func TestWrite_CreatesNewFile(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "new.txt")
	out, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "",
		"new_block": "hello\nworld\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WriteResult)
	if !res.Created || res.BytesAfter != len("hello\nworld\n") {
		t.Fatalf("bad result: %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\nworld\n" {
		t.Fatalf("file content wrong: %q", string(got))
	}
}

func TestWrite_RefusesToOverwriteExistingWithEmptyOld(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(path, []byte("important data"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "",
		"new_block": "garbage",
	})
	if err == nil {
		t.Fatal("expected error: empty old_block must not overwrite existing file")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "important data" {
		t.Fatalf("file was modified despite refusal: %q", string(got))
	}
}

func TestWrite_ReplacesUniqueBlock(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	original := "package main\n\nfunc Hello() string {\n    return \"old\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "    return \"old\"",
		"new_block": "    return \"new\"",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WriteResult)
	if res.Created {
		t.Fatal("should not be created")
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), `return "new"`) {
		t.Fatalf("replacement did not happen: %q", string(got))
	}
	if strings.Contains(string(got), `return "old"`) {
		t.Fatalf("old content still present: %q", string(got))
	}
}

func TestWrite_ErrorsWhenAnchorNotFound(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "no such text",
		"new_block": "x",
	})
	if err == nil {
		t.Fatal("expected error when anchor not in file")
	}
	if !strings.Contains(err.Error(), "read the file") {
		t.Errorf("error should ask agent to read first; got: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Fatalf("file modified despite error: %q", string(got))
	}
}

func TestWrite_ErrorsOnAmbiguousAnchor(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "ambiguous.go")
	content := "x = 1\nx = 1\nx = 1\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "x = 1",
		"new_block": "x = 2",
	})
	if err == nil {
		t.Fatal("expected error on ambiguous anchor")
	}
	if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "matches 3 times") {
		t.Errorf("error should mention ambiguity; got: %v", err)
	}
}

func TestWrite_DeletesBlockWhenNewIsEmpty(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "del.txt")
	if err := os.WriteFile(path, []byte("keep\nremove me\nkeep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "remove me\n",
		"new_block": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "keep\nkeep\n" {
		t.Fatalf("deletion failed: %q", string(got))
	}
}

func TestWrite_DryRunPreviewDoesNotModify(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	original := "package main\n\nfunc Hello() string {\n    return \"old\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "write", map[string]any{
		"path":      path,
		"old_block": "    return \"old\"",
		"new_block": "    return \"new\"",
		"dry_run":   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WriteResult)
	if !res.DryRun {
		t.Fatal("expected DryRun=true")
	}
	if !strings.Contains(res.Diff, `-    return "old"`) || !strings.Contains(res.Diff, `+    return "new"`) {
		t.Fatalf("diff missing expected lines:\n%s", res.Diff)
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("dry_run must not modify the file, got: %q", string(got))
	}
}

func TestWrite_DryRunIsLowRisk(t *testing.T) {
	tool, _ := BuiltIn().Get("write")
	if got := AssessRisk(tool, map[string]any{"dry_run": true}); got != RiskLow {
		t.Fatalf("dry_run write should be RiskLow, got %v", got)
	}
	if got := AssessRisk(tool, map[string]any{}); got != RiskHigh {
		t.Fatalf("real write should be RiskHigh, got %v", got)
	}
}

// ---------- ReadTool (composite) ----------

func TestRead_FullFileWithLineNumbers(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{
		"path": path,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(ReadResult)
	if res.LineCount != 3 {
		t.Fatalf("expected 3 lines, got %d (%+v)", res.LineCount, res)
	}
	if res.TotalLines != 3 {
		t.Errorf("total wrong: %d", res.TotalLines)
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		marker := fmt.Sprintf("%6d→%s", i+1, want)
		if !strings.Contains(res.Content, marker) {
			t.Errorf("missing %q in:\n%s", marker, res.Content)
		}
	}
}

func TestRead_RangeIsRespected(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "ten.txt")
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("L%02d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{
		"path":  path,
		"start": 3,
		"end":   5,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(ReadResult)
	if res.LineCount != 3 {
		t.Fatalf("expected 3 lines, got %d", res.LineCount)
	}
	for _, want := range []string{"L03", "L04", "L05"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, banned := range []string{"L01", "L02", "L06"} {
		if strings.Contains(res.Content, banned) {
			t.Errorf("range leaked: %q", banned)
		}
	}
}

func TestRead_TruncationHonorsMaxLines(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "many.txt")
	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("L%02d", i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{
		"path":      path,
		"max_lines": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(ReadResult)
	if !res.Truncated {
		t.Fatalf("expected Truncated=true; got %+v", res)
	}
	if res.LineCount != 5 {
		t.Fatalf("expected 5 lines after truncation, got %d", res.LineCount)
	}
}

func TestRead_RawMode(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.Call(context.Background(), "read", map[string]any{
		"path": path,
		"raw":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(ReadResult)
	// raw should NOT contain the line-number prefix (no "→").
	if strings.Contains(res.Content, "→") {
		t.Fatalf("raw mode should omit line numbers; got %q", res.Content)
	}
	if !strings.Contains(res.Content, "alpha") || !strings.Contains(res.Content, "beta") || !strings.Contains(res.Content, "gamma") {
		t.Fatalf("raw mode missing content: %q", res.Content)
	}
}

// ---------- MemoryTool (third-level composite over read + write) ----------

func TestMemory_CreatesNewEntry(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("MINI_MEMORY_DIR", memDir)
	r := BuiltIn()

	out, err := r.Call(context.Background(), "memory", map[string]any{
		"name":    "user_prefs",
		"content": "User prefers concise responses in Spanish.",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(MemoryResult)
	if !res.Created {
		t.Fatalf("expected Created=true, got %+v", res)
	}
	expectedPath := filepath.Join(memDir, "user_prefs.md")
	if res.Path != expectedPath {
		t.Errorf("path: got %q want %q", res.Path, expectedPath)
	}
	body, _ := os.ReadFile(expectedPath)
	if string(body) != "User prefers concise responses in Spanish." {
		t.Fatalf("file content wrong: %q", string(body))
	}
}

func TestMemory_SetReplacesExisting(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("MINI_MEMORY_DIR", memDir)
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := BuiltIn()
	out, err := r.Call(context.Background(), "memory", map[string]any{
		"name":    "notes",
		"content": "completely new content",
		"mode":    "set",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(MemoryResult)
	if res.Created {
		t.Fatalf("Created should be false when overwriting; got %+v", res)
	}
	body, _ := os.ReadFile(filepath.Join(memDir, "notes.md"))
	if string(body) != "completely new content" {
		t.Fatalf("expected replacement, got %q", string(body))
	}
}

func TestMemory_AppendKeepsExistingContent(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("MINI_MEMORY_DIR", memDir)
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("first fact"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := BuiltIn()
	_, err := r.Call(context.Background(), "memory", map[string]any{
		"name":    "notes",
		"content": "second fact",
		"mode":    "append",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(memDir, "notes.md"))
	if !strings.Contains(string(body), "first fact") {
		t.Errorf("first fact lost: %q", string(body))
	}
	if !strings.Contains(string(body), "second fact") {
		t.Errorf("second fact missing: %q", string(body))
	}
	// Verify there's a blank line separating them.
	if !strings.Contains(string(body), "first fact\n\nsecond fact") {
		t.Errorf("expected blank-line separator, got %q", string(body))
	}
}

func TestMemory_RejectsInvalidName(t *testing.T) {
	t.Setenv("MINI_MEMORY_DIR", t.TempDir())
	r := BuiltIn()
	for _, bad := range []string{"../escape", "with space", "name/slash", ".hidden", "", "with.dots"} {
		_, err := r.Call(context.Background(), "memory", map[string]any{
			"name":    bad,
			"content": "x",
		})
		if err == nil {
			t.Errorf("expected error for invalid name %q", bad)
		}
	}
}

func TestMemory_RejectsBadMode(t *testing.T) {
	t.Setenv("MINI_MEMORY_DIR", t.TempDir())
	r := BuiltIn()
	_, err := r.Call(context.Background(), "memory", map[string]any{
		"name":    "x",
		"content": "y",
		"mode":    "invalid",
	})
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestMemory_DependsOnReadAndWrite(t *testing.T) {
	r := BuiltIn()
	tl, _ := r.Get("memory")
	deps := tl.DependsOn()
	if len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %v", deps)
	}
	wantSet := map[string]bool{"read": false, "write": false}
	for _, d := range deps {
		if _, ok := wantSet[d]; !ok {
			t.Errorf("unexpected dep %q", d)
		}
		wantSet[d] = true
	}
	for d, seen := range wantSet {
		if !seen {
			t.Errorf("missing dep %q", d)
		}
	}
}

// LoadMemoryContent: read all *.md, alphabetical order, joined with blank lines.
func TestLoadMemoryContent(t *testing.T) {
	memDir := t.TempDir()
	t.Setenv("MINI_MEMORY_DIR", memDir)

	// Create three memory files. Note the second is .txt (not .md) and should
	// be ignored.
	files := map[string]string{
		"a.md":  "I am the first fact.",
		"z.md":  "I am the last fact.",
		"m.md":  "I am the middle fact.",
		"x.txt": "I should be ignored (not .md).",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(memDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := LoadMemoryContent()
	if err != nil {
		t.Fatal(err)
	}
	want := "I am the first fact.\n\nI am the middle fact.\n\nI am the last fact."
	if got != want {
		t.Fatalf("content mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestLoadMemoryContent_EmptyOrMissingDir(t *testing.T) {
	t.Setenv("MINI_MEMORY_DIR", filepath.Join(t.TempDir(), "does-not-exist"))
	got, err := LoadMemoryContent()
	if err != nil {
		t.Fatalf("missing dir should be ok, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty content, got %q", got)
	}
}

// ---------- GlobTool (composite) ----------

func TestSplitGlob(t *testing.T) {
	cases := []struct {
		in            string
		wantPrefix    string
		wantName      string
		wantRecursive bool
	}{
		{"*.go", "", "*.go", false},
		{"src/*.ts", "src", "*.ts", false},
		{"**/*.go", "", "*.go", true},
		{"src/**/*.go", "src", "*.go", true},
		{"src/**/foo/*.ts", "src", "*.ts", true},
		{"go.mod", "", "go.mod", false},
	}
	for _, tc := range cases {
		gotPrefix, gotName, gotRec := splitGlob(tc.in)
		if gotPrefix != tc.wantPrefix || gotName != tc.wantName || gotRec != tc.wantRecursive {
			t.Errorf("splitGlob(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tc.in, gotPrefix, gotName, gotRec, tc.wantPrefix, tc.wantName, tc.wantRecursive)
		}
	}
}

func TestGlob_NonRecursive(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "c.txt", "sub/deep.go"} {
		full := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	out, err := r.Call(context.Background(), "glob", map[string]any{
		"pattern": "*.go",
		"path":    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(GlobResult)
	if res.Total != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", res.Total, res.Paths)
	}
}

func TestGlob_Recursive(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	for _, f := range []string{"a.go", "sub/b.go", "sub/deep/c.go", "sub/d.txt"} {
		full := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	out, err := r.Call(context.Background(), "glob", map[string]any{
		"pattern": "**/*.go",
		"path":    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(GlobResult)
	if res.Total != 3 {
		t.Fatalf("expected 3 matches, got %d: %v", res.Total, res.Paths)
	}
}

func TestGlob_TypeDirectory(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	for _, f := range []string{"alpha/keep.txt", "beta/keep.txt", "gamma/keep.txt"} {
		full := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	out, err := r.Call(context.Background(), "glob", map[string]any{
		"pattern": "*",
		"path":    dir,
		"type":    "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(GlobResult).Total != 3 {
		t.Fatalf("expected 3 dirs; got %+v", out)
	}
}

func TestGlob_HiddenFilter(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	for _, f := range []string{"visible.txt", ".hidden.txt", ".dotdir/inside.txt"} {
		full := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	out, err := r.Call(context.Background(), "glob", map[string]any{
		"pattern": "**/*",
		"path":    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range out.(GlobResult).Paths {
		if strings.HasPrefix(filepath.Base(p), ".") || strings.Contains(p, "/.dotdir/") {
			t.Errorf("hidden leaked: %s", p)
		}
	}
}

func TestGlob_NoMatchesReturnsEmpty(t *testing.T) {
	r := BuiltIn()
	dir := t.TempDir()
	out, err := r.Call(context.Background(), "glob", map[string]any{
		"pattern": "*.nope",
		"path":    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(GlobResult).Total != 0 {
		t.Fatalf("expected empty; got %+v", out)
	}
}

// ---------- TaskTool (composite, sub-agent spawner) ----------
//
// We can't run the real `mini` binary in unit tests (would call Gemini and is
// flaky / requires creds). Instead, we use a recordingCaller to verify that
// `task` correctly delegates to `bash` with a `mini chat <prompt>` shape.

func TestTask_DelegatesToBashWithMiniChat(t *testing.T) {
	r := BuiltIn()
	rc := &recordingCaller{inner: r, intercept: map[string]any{
		"bash": BashResult{Stdout: "(stub)", ExitCode: 0, Description: "stubbed"},
	}}
	tk, _ := r.Get("task")
	_, err := tk.Execute(context.Background(), map[string]any{
		"prompt": "investigate auth flow",
	}, rc)
	if err != nil {
		t.Fatalf("task execute: %v", err)
	}
	if len(rc.calls) != 1 || rc.calls[0].name != "bash" {
		t.Fatalf("expected exactly one bash delegation, got %v", rc.callNames())
	}
	cmd, _ := rc.calls[0].args["command"].(string)
	if !strings.Contains(cmd, "chat") {
		t.Errorf("expected command to invoke `chat`, got: %s", cmd)
	}
	if !strings.Contains(cmd, "investigate auth flow") {
		t.Errorf("prompt not in command: %s", cmd)
	}
	desc, _ := rc.calls[0].args["description"].(string)
	if !strings.Contains(desc, "Sub-agent") {
		t.Errorf("description should mark this as a sub-agent task: %s", desc)
	}
	// The new task tool routes default (background=false) to `bash`, not
	// to bash with background:false. Verify it called bash (not bash_input).
	if rc.calls[0].name != "bash" {
		t.Errorf("foreground task should call bash, got: %s", rc.calls[0].name)
	}
}


// ---------- Process tracker / shutdown ----------



// ---------- TaskTool: foreground passes timeout=0 ----------

func TestTask_ForegroundPassesNoTimeout(t *testing.T) {
	r := BuiltIn()
	rc := &recordingCaller{inner: r, intercept: map[string]any{
		"bash": BashResult{Stdout: "(stub)", ExitCode: 0},
	}}
	tk, _ := r.Get("task")
	_, err := tk.Execute(context.Background(), map[string]any{
		"prompt": "long investigation",
	}, rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 1 {
		t.Fatalf("expected 1 bash call, got %d", len(rc.calls))
	}
	got, ok := rc.calls[0].args["timeout_seconds"]
	if !ok {
		t.Fatal("task should have set timeout_seconds=0 in foreground")
	}
	if got != 0 {
		t.Fatalf("expected timeout_seconds=0, got %v", got)
	}
}


// ---------- Composition guarantee: composites really go via Caller ----------

type recordedCall struct {
	name string
	args map[string]any
}

type recordingCaller struct {
	inner     *Registry
	calls     []recordedCall
	intercept map[string]any // optional canned responses by tool name
}

func (rc *recordingCaller) callNames() []string {
	out := []string{}
	for _, c := range rc.calls {
		out = append(out, c.name)
	}
	return out
}

func (rc *recordingCaller) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	rc.calls = append(rc.calls, recordedCall{name: name, args: args})
	if rc.intercept != nil {
		if v, ok := rc.intercept[name]; ok {
			return v, nil
		}
	}
	return rc.inner.Call(ctx, name, args)
}
func (rc *recordingCaller) Has(name string) bool { return rc.inner.Has(name) }

func TestComposite_GoesThroughCaller(t *testing.T) {
	r := BuiltIn()
	rc := &recordingCaller{inner: r}

	// read goes through bash
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)
	rd, _ := r.Get("read")
	if _, err := rd.Execute(context.Background(), map[string]any{"path": path}, rc); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 1 || rc.calls[0].name != "bash" {
		t.Fatalf("read should call bash exactly once; got %v", rc.callNames())
	}

	rc.calls = nil
	gl, _ := r.Get("glob")
	if _, err := gl.Execute(context.Background(), map[string]any{
		"pattern": "*.txt", "path": dir,
	}, rc); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 1 || rc.calls[0].name != "bash" {
		t.Fatalf("glob should call bash exactly once; got %v", rc.callNames())
	}
}
