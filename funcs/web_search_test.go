package funcs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A minimal HTML snippet mirroring the parts of DDG's response we depend on.
// We exercise both wrapped (/l/?uddg=...) and direct hrefs to make sure the
// parser handles both shapes.
const ddgHTMLSample = `<!DOCTYPE html><html><body>
<div class="results">
  <div class="result results_links results_links_deep web-result">
    <div class="result__body links_main links_deep">
      <h2 class="result__title">
        <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Falpha&amp;rut=abc">
          Alpha <b>title</b>
        </a>
      </h2>
      <a class="result__snippet" href="...">Alpha &amp; snippet content.</a>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result">
    <div class="result__body links_main links_deep">
      <h2 class="result__title">
        <a class="result__a" href="https://example.org/beta">Beta direct</a>
      </h2>
      <div class="result__snippet">Beta description here.</div>
    </div>
  </div>
  <div class="result results_links_deep web-result">
    <div class="result__body">
      <h2><a class="result__a" href="https://example.com/alpha">Alpha dup</a></h2>
      <div class="result__snippet">Same URL as first; should be deduped.</div>
    </div>
  </div>
</div></body></html>`

func TestWebSearch_ParsesDDG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = r.ParseForm()
		if r.FormValue("q") != "hello world" {
			t.Errorf("query not forwarded: %q", r.FormValue("q"))
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(ddgHTMLSample))
	}))
	defer srv.Close()
	t.Setenv("MINI_WEB_SEARCH_URL", srv.URL)

	r := BuiltIn()
	out, err := r.Call(context.Background(), "web_search", map[string]any{
		"query": "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WebSearchResult)
	if len(res.Hits) != 2 {
		t.Fatalf("expected 2 hits (dedup), got %d: %+v", len(res.Hits), res.Hits)
	}
	if res.Hits[0].URL != "https://example.com/alpha" {
		t.Errorf("first url: %q (uddg decode failed?)", res.Hits[0].URL)
	}
	if !strings.Contains(res.Hits[0].Title, "Alpha") || !strings.Contains(res.Hits[0].Title, "title") {
		t.Errorf("first title not extracted: %q", res.Hits[0].Title)
	}
	if res.Hits[0].Snippet != "Alpha & snippet content." {
		t.Errorf("first snippet: %q", res.Hits[0].Snippet)
	}
	if res.Hits[1].URL != "https://example.org/beta" || res.Hits[1].Snippet != "Beta description here." {
		t.Errorf("second hit wrong: %+v", res.Hits[1])
	}
}

func TestWebSearch_MaxResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ddgHTMLSample))
	}))
	defer srv.Close()
	t.Setenv("MINI_WEB_SEARCH_URL", srv.URL)

	r := BuiltIn()
	out, err := r.Call(context.Background(), "web_search", map[string]any{
		"query":       "x",
		"max_results": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out.(WebSearchResult).Hits); got != 1 {
		t.Fatalf("expected 1 hit after cap, got %d", got)
	}
}

func TestWebSearch_EngineStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	t.Setenv("MINI_WEB_SEARCH_URL", srv.URL)

	r := BuiltIn()
	_, err := r.Call(context.Background(), "web_search", map[string]any{"query": "x"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected 503 error, got %v", err)
	}
}

func TestDecodeDDGHref(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.com%2Fb&rut=x", "https://a.com/b"},
		{"https://direct.example/path", "https://direct.example/path"},
		{"//cdn.example/x", "https://cdn.example/x"},
	}
	for _, c := range cases {
		if got := decodeDDGHref(c.in); got != c.want {
			t.Errorf("decodeDDGHref(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
