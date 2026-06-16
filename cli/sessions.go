package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arcaela/mini-cli/config"
	"github.com/arcaela/mini-cli/provider"
)

// =============================================================================
// Session persistence
//
// A session is the conversation `History` of a REPL run, saved as JSONL at
// `~/.mini/sessions/<id>.jsonl`. The id is the save-time timestamp + a slug
// (so listings sort chronologically and stay human-recognisable).
//
// Format: one provider.Message per line. JSONL keeps `/load` cheap even for
// long sessions (line-by-line decoding) and the format survives schema
// extensions to provider.Message because unknown fields are ignored.
// =============================================================================

// SessionMeta describes an on-disk session in listings.
type SessionMeta struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	SavedAt  time.Time `json:"saved_at"`
	Messages int       `json:"messages"`
	Size     int64     `json:"size_bytes"`
}

func sessionsDir() string {
	if v := os.Getenv("MINI_SESSIONS_DIR"); v != "" {
		return v
	}
	return filepath.Join(config.ConfigDir(), "sessions")
}

// SaveSession writes `history` as JSONL to ~/.mini/sessions/<id>.jsonl.
// `name` is an optional human label; if empty, only the timestamp is used.
// Returns the resolved file path.
func SaveSession(name string, history []provider.Message) (string, error) {
	if err := os.MkdirAll(sessionsDir(), 0o700); err != nil {
		return "", fmt.Errorf("create sessions dir: %w", err)
	}
	id := newSessionID(name)
	path := filepath.Join(sessionsDir(), id+".jsonl")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, m := range history {
		if err := enc.Encode(m); err != nil {
			return "", fmt.Errorf("encode message: %w", err)
		}
	}
	return path, nil
}

// LoadSession returns the history stored under `id` (with or without the
// `.jsonl` extension). Unknown id → error.
func LoadSession(id string) ([]provider.Message, error) {
	path, err := resolveSessionID(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := []provider.Message{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m provider.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("session %s line %d: %w", id, i+1, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// ListSessions returns every saved session, newest first.
func ListSessions() ([]SessionMeta, error) {
	dir := sessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []SessionMeta{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		out = append(out, SessionMeta{
			ID:       strings.TrimSuffix(e.Name(), ".jsonl"),
			Path:     full,
			SavedAt:  info.ModTime(),
			Messages: countLines(full),
			Size:     info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out, nil
}

// resolveSessionID accepts a bare id, a `prefix*` partial match, or a full
// path. Helpful for users who only remember the slug.
func resolveSessionID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("session id is required")
	}
	// Already a full path?
	if strings.Contains(id, "/") {
		if _, err := os.Stat(id); err == nil {
			return id, nil
		}
	}
	// id.jsonl in the standard dir.
	exact := filepath.Join(sessionsDir(), id+".jsonl")
	if _, err := os.Stat(exact); err == nil {
		return exact, nil
	}
	// Prefix match — useful if user types just the timestamp portion.
	sessions, err := ListSessions()
	if err != nil {
		return "", err
	}
	var hits []string
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, id) {
			hits = append(hits, s.Path)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no session matches %q", id)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("%q is ambiguous (%d matches); be more specific", id, len(hits))
	}
}

// newSessionID composes a sortable, human-friendly id. `name` is slugified
// (lowercase, alnum/dash only) and capped so filenames stay sane.
func newSessionID(name string) string {
	stamp := time.Now().UTC().Format("20060102T150405")
	if name == "" {
		return stamp
	}
	return stamp + "-" + slugify(name)
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = out[:48]
	}
	if out == "" {
		out = "session"
	}
	return out
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
