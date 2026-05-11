package funcs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// =============================================================================
// web_fetch — base function: GET a URL and return its text content.
//
// Pure stdlib (zero deps). HTML→text is a small heuristic: strip
// <script>/<style>, drop other tags, collapse whitespace. Enough for an
// agent reading prose. Pass `format: "raw"` if the model wants to parse
// the HTML itself.
// =============================================================================

type webFetchTool struct{}

func init() { Register(webFetchTool{}) }

func (webFetchTool) Name() string        { return "web_fetch" }
func (webFetchTool) Kind() Kind          { return KindBase }
func (webFetchTool) DependsOn() []string { return nil }
func (webFetchTool) Description() string {
	return "Fetch a URL with HTTP GET and return its text content. Default format `text` " +
		"(HTML stripped, whitespace collapsed); pass `format: \"raw\"` for the raw response body. " +
		"Follows up to 10 redirects. Default timeout 30s, max 120s. Caps response at 1 MiB."
}

func (webFetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http(s) URL.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Wall-clock cap (default 30, max 120).",
			},
			"format": map[string]any{
				"type":        "string",
				"description": "`text` (default, HTML stripped) or `raw` (response body as-is).",
				"enum":        []string{"text", "raw"},
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Hard cap on bytes read (default 1048576 = 1 MiB).",
			},
		},
		"required": []string{"url"},
	}
}

type WebFetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	Format      string `json:"format"`
	Bytes       int    `json:"bytes"`
	Truncated   bool   `json:"truncated,omitempty"`
	Content     string `json:"content"`
}

const (
	webFetchDefaultTimeout = 30
	webFetchMaxTimeout     = 120
	webFetchDefaultMax     = 1 << 20 // 1 MiB
	webFetchMaxRedirects   = 10
)

func (webFetchTool) Execute(ctx context.Context, args map[string]any, _ Caller) (any, error) {
	target, err := argRequiredString(args, "url")
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid url %q: needs to be http(s)", target)
	}
	timeout, err := argInt(args, "timeout_seconds", webFetchDefaultTimeout)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > webFetchMaxTimeout {
		timeout = webFetchDefaultTimeout
	}
	format, err := argString(args, "format", "text")
	if err != nil {
		return nil, err
	}
	if format != "text" && format != "raw" {
		return nil, fmt.Errorf(`format must be "text" or "raw" (got %q)`, format)
	}
	maxBytes, err := argInt(args, "max_bytes", webFetchDefaultMax)
	if err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = webFetchDefaultMax
	}

	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webFetchMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", webFetchMaxRedirects)
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	// Plain UA so basic sites do not 403 us; not pretending to be a real browser.
	req.Header.Set("User-Agent", "mini-cli/1.0 (+https://github.com/arcaela/mini-cli)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer res.Body.Close()

	// Read up to maxBytes+1 so we can detect truncation.
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	truncated := false
	if len(body) > maxBytes {
		body = body[:maxBytes]
		truncated = true
	}

	out := WebFetchResult{
		URL:         target,
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Format:      format,
		Bytes:       len(body),
		Truncated:   truncated,
	}
	if res.Request != nil && res.Request.URL != nil && res.Request.URL.String() != target {
		out.FinalURL = res.Request.URL.String()
	}
	switch format {
	case "raw":
		out.Content = string(body)
	case "text":
		out.Content = htmlToText(string(body))
	}
	return out, nil
}

// htmlToText is a minimal HTML→text converter. Heuristic on purpose:
//   - drop <script>/<style> blocks
//   - turn block tags into newlines
//   - strip remaining tags
//   - decode the common HTML entities
//   - collapse runs of whitespace
//
// Good enough for prose; not for tables or code blocks.
var (
	// Go's RE2 has no backreferences, so we use two regexes instead of one
	// with `\1`. Both run with (?is): case-insensitive + . matches newlines.
	reScriptBlock = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStyleBlock  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reBlockTag    = regexp.MustCompile(`(?i)</?(p|div|li|ul|ol|h[1-6]|br|tr|th|td|hr|article|section|nav|header|footer|main|aside|pre|blockquote)\b[^>]*>`)
	reAnyTag      = regexp.MustCompile(`<[^>]+>`)
	reMultiBlank  = regexp.MustCompile(`\n{3,}`)
	reHorizSpace  = regexp.MustCompile(`[ \t]+`)
	reTrailSpace  = regexp.MustCompile(`(?m)[ \t]+$`)
)

func htmlToText(s string) string {
	s = reScriptBlock.ReplaceAllString(s, "")
	s = reStyleBlock.ReplaceAllString(s, "")
	s = reBlockTag.ReplaceAllString(s, "\n")
	s = reAnyTag.ReplaceAllString(s, "")
	for _, kv := range []struct{ from, to string }{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", `"`}, {"&#39;", "'"}, {"&apos;", "'"},
		{"&nbsp;", " "}, {"&#160;", " "},
	} {
		s = strings.ReplaceAll(s, kv.from, kv.to)
	}
	s = reHorizSpace.ReplaceAllString(s, " ")
	s = reTrailSpace.ReplaceAllString(s, "")
	s = reMultiBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
