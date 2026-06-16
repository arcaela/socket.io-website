package funcs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetch_RawAndText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><head><title>t</title><style>body{}</style><script>x()</script></head>
<body><h1>Hola</h1><p>mundo &amp; <b>negrita</b></p></body></html>`)
	}))
	defer srv.Close()

	r := BuiltIn()

	// Default format = text: tags stripped, entities decoded.
	out, err := r.Call(context.Background(), "web_fetch", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WebFetchResult)
	if res.Status != 200 {
		t.Fatalf("status=%d", res.Status)
	}
	if !strings.Contains(res.Content, "Hola") || !strings.Contains(res.Content, "mundo & negrita") {
		t.Fatalf("text not extracted: %q", res.Content)
	}
	if strings.Contains(res.Content, "<") || strings.Contains(res.Content, "script") {
		t.Fatalf("tags/script not stripped: %q", res.Content)
	}

	// Raw format keeps HTML verbatim.
	out, err = r.Call(context.Background(), "web_fetch", map[string]any{"url": srv.URL, "format": "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.(WebFetchResult).Content, "<script>") {
		t.Fatal("raw format should preserve HTML")
	}
}

func TestWebFetch_FollowsRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<p>landed</p>")
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer first.Close()

	r := BuiltIn()
	out, err := r.Call(context.Background(), "web_fetch", map[string]any{"url": first.URL})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WebFetchResult)
	if res.FinalURL == "" {
		t.Fatal("FinalURL should be set on redirect")
	}
	if !strings.Contains(res.Content, "landed") {
		t.Fatalf("did not follow redirect: %q", res.Content)
	}
}

func TestWebFetch_Truncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
	}))
	defer srv.Close()
	r := BuiltIn()
	out, err := r.Call(context.Background(), "web_fetch", map[string]any{
		"url": srv.URL, "format": "raw", "max_bytes": 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(WebFetchResult)
	if !res.Truncated || res.Bytes != 100 {
		t.Fatalf("expected truncated to 100 bytes, got %+v", res)
	}
}

func TestWebFetch_RejectsNonHTTP(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "web_fetch", map[string]any{"url": "file:///etc/passwd"})
	if err == nil {
		t.Fatal("expected error for file:// scheme")
	}
}

func TestWebFetch_4xxReturnsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = fmt.Fprint(w, "<p>not found</p>")
	}))
	defer srv.Close()
	r := BuiltIn()
	out, err := r.Call(context.Background(), "web_fetch", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("4xx should not be a Go error, body is useful: %v", err)
	}
	res := out.(WebFetchResult)
	if res.Status != 404 || !strings.Contains(res.Content, "not found") {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestHtmlToText_BasicShape(t *testing.T) {
	in := `<html><body>
<h1>Title</h1>
<p>First &amp; only paragraph.</p>
<script>alert(1)</script>
<style>p{color:red}</style>
<p>Second.</p>
</body></html>`
	out := htmlToText(in)
	for _, want := range []string{"Title", "First & only paragraph.", "Second."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, banned := range []string{"<", ">", "alert(", "color:red"} {
		if strings.Contains(out, banned) {
			t.Errorf("banned %q present in:\n%s", banned, out)
		}
	}
}
