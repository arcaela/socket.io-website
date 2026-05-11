package funcs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// =============================================================================
// web_search — base function: search the web via DuckDuckGo's HTML endpoint.
//
// No API key required. We POST to `https://html.duckduckgo.com/html/` and
// parse the result blocks out of the response. DDG's HTML changes from time
// to time, so the parsing here is intentionally tolerant: it looks for the
// stable anchor classes (`result__a`, `result__snippet`) and accepts either
// direct or wrapped (`/l/?uddg=...`) hrefs.
//
// Swap engines: set MINI_WEB_SEARCH_URL to a different HTML-serving
// endpoint (e.g. a self-hosted SearXNG instance) if DDG ever blocks us.
// =============================================================================

type webSearchTool struct{}

func init() { Register(webSearchTool{}) }

func (webSearchTool) Name() string        { return "web_search" }
func (webSearchTool) Kind() Kind          { return KindBase }
func (webSearchTool) DependsOn() []string { return nil }
func (webSearchTool) Description() string {
	return "Search the web and return a ranked list of {title, url, snippet} results. " +
		"Backed by DuckDuckGo's HTML endpoint (no API key needed). Use this to find sources, " +
		"check facts, or discover URLs to pass to `web_fetch` for full content."
}

func (webSearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Free-text search query.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Cap on the number of results returned (default 10, max 25).",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Wall-clock cap (default 15, max 60).",
			},
		},
		"required": []string{"query"},
	}
}

type WebSearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

type WebSearchResult struct {
	Query  string         `json:"query"`
	Engine string         `json:"engine"`
	Hits   []WebSearchHit `json:"hits"`
}

const (
	webSearchDefaultMax     = 10
	webSearchHardMax        = 25
	webSearchDefaultTimeout = 15
	webSearchMaxTimeout     = 60
	ddgHTMLEndpoint         = "https://html.duckduckgo.com/html/"
)

func (webSearchTool) Execute(ctx context.Context, args map[string]any, _ Caller) (any, error) {
	query, err := argRequiredString(args, "query")
	if err != nil {
		return nil, err
	}
	maxResults, err := argInt(args, "max_results", webSearchDefaultMax)
	if err != nil {
		return nil, err
	}
	if maxResults <= 0 {
		maxResults = webSearchDefaultMax
	}
	if maxResults > webSearchHardMax {
		maxResults = webSearchHardMax
	}
	timeout, err := argInt(args, "timeout_seconds", webSearchDefaultTimeout)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > webSearchMaxTimeout {
		timeout = webSearchDefaultTimeout
	}

	endpoint := ddgHTMLEndpoint
	if v := webSearchEndpoint(); v != "" {
		endpoint = v
	}

	body := url.Values{}
	body.Set("q", query)
	body.Set("kl", "wt-wt") // no regional bias

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "mini-cli/1.0 (+https://github.com/arcaela/mini-cli)")
	req.Header.Set("Accept", "text/html")

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("search engine returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20)) // 4 MiB ceiling
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	hits := parseDDGResults(string(raw), maxResults)
	return WebSearchResult{
		Query:  query,
		Engine: "duckduckgo",
		Hits:   hits,
	}, nil
}

// webSearchEndpoint allows overriding the engine without touching code.
func webSearchEndpoint() string {
	// Looked up via env to keep this tool self-contained.
	return getenv("MINI_WEB_SEARCH_URL")
}

// =============================================================================
// DDG HTML parser
//
// Stable enough patterns: each result is wrapped in <div class="result"...>
// (or "results__a__not_no" variants) and contains an anchor with class
// `result__a` for the title and a `result__snippet` for the snippet.
// =============================================================================

var (
	// Locate ALL title anchors (one per result) using FindAllStringSubmatchIndex
	// so we know where each lives in the HTML and can look ahead for the
	// nearest snippet without trying to delimit a "result block" with a
	// fragile balanced-tag regex.
	reDDGTitleAnchor = regexp.MustCompile(`(?is)<a[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*\bhref="([^"]+)"[^>]*>(.*?)</a>`)
	reDDGSnippet     = regexp.MustCompile(`(?is)<(?:a|div|span)[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</(?:a|div|span)>`)
	reDDGUddg        = regexp.MustCompile(`[?&]uddg=([^&"]+)`)
	reCompactSpace   = regexp.MustCompile(`\s+`)
)

// snippetWindowBytes is how far ahead of a title anchor we look for the
// matching snippet. DDG result blocks are well under this; the window
// also prevents accidentally grabbing a snippet from the next result.
const snippetWindowBytes = 1500

// parseDDGResults walks each title anchor in order and pairs it with the
// nearest following snippet (within snippetWindowBytes). Deduplicates by URL.
func parseDDGResults(html string, max int) []WebSearchHit {
	seen := map[string]bool{}
	hits := []WebSearchHit{}

	titleMatches := reDDGTitleAnchor.FindAllStringSubmatchIndex(html, -1)
	for _, m := range titleMatches {
		// m[2..3] = href group, m[4..5] = title group, m[1] = end of <a...</a>
		href := decodeDDGHref(html[m[2]:m[3]])
		title := stripTags(html[m[4]:m[5]])
		if href == "" || title == "" || seen[href] {
			continue
		}
		seen[href] = true

		snippet := ""
		windowEnd := m[1] + snippetWindowBytes
		if windowEnd > len(html) {
			windowEnd = len(html)
		}
		if sm := reDDGSnippet.FindStringSubmatch(html[m[1]:windowEnd]); sm != nil {
			snippet = stripTags(sm[1])
		}

		hits = append(hits, WebSearchHit{Title: title, URL: href, Snippet: snippet})
		if len(hits) >= max {
			break
		}
	}
	return hits
}

var _ = sort.SliceStable // keep stable import surface if we re-add sorting later

// decodeDDGHref unwraps DDG's tracking redirect (`/l/?uddg=ENCODED`) when
// present and returns a plain absolute URL. Direct hrefs are returned as-is.
func decodeDDGHref(raw string) string {
	if m := reDDGUddg.FindStringSubmatch(raw); m != nil {
		if u, err := url.QueryUnescape(m[1]); err == nil {
			return u
		}
	}
	// Sometimes DDG returns `//host/...` — promote to https.
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return strings.TrimSpace(raw)
}

// stripTags is a tiny HTML→text for inline title/snippet text. Reuses the
// same heuristics as web_fetch but local so neither tool depends on the
// other's internal state.
func stripTags(s string) string {
	s = reAnyTag.ReplaceAllString(s, " ")
	for _, kv := range [...][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", `"`}, {"&#39;", "'"}, {"&apos;", "'"},
		{"&nbsp;", " "}, {"&#160;", " "},
	} {
		s = strings.ReplaceAll(s, kv[0], kv[1])
	}
	s = reCompactSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// getenv reads the environment; isolated so tests can stub via t.Setenv.
func getenv(name string) string {
	return os.Getenv(name)
}
