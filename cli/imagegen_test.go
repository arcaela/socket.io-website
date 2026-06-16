package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

type fakeImageGen struct {
	imgs []provider.Image
	err  error
}

func (f fakeImageGen) GenerateImage(context.Context, string, provider.ImageGenOptions) ([]provider.Image, error) {
	return f.imgs, f.err
}

func TestImageGenTool_SavesSingleImage(t *testing.T) {
	tool := NewImageGenTool(fakeImageGen{imgs: []provider.Image{{MimeType: "image/png", Data: []byte("PNGDATA")}}})
	dir := t.TempDir()
	out := filepath.Join(dir, "art.png")

	res, err := tool.Execute(context.Background(), map[string]any{"prompt": "a cat", "path": out}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res.(imageGenResult)
	if r.Count != 1 || len(r.Paths) != 1 || r.Paths[0] != out {
		t.Fatalf("unexpected result: %+v", r)
	}
	if data, _ := os.ReadFile(out); string(data) != "PNGDATA" {
		t.Fatalf("file content wrong: %q", string(data))
	}
}

func TestImageGenTool_MultipleImagesGetIndexedPaths(t *testing.T) {
	tool := NewImageGenTool(fakeImageGen{imgs: []provider.Image{
		{MimeType: "image/png", Data: []byte("A")},
		{MimeType: "image/png", Data: []byte("B")},
	}})
	dir := t.TempDir()
	base := filepath.Join(dir, "pic.png")

	res, err := tool.Execute(context.Background(), map[string]any{"prompt": "two", "path": base, "count": 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res.(imageGenResult)
	if r.Count != 2 {
		t.Fatalf("expected 2 images, got %d", r.Count)
	}
	for i, want := range []string{filepath.Join(dir, "pic-1.png"), filepath.Join(dir, "pic-2.png")} {
		if r.Paths[i] != want {
			t.Errorf("path %d = %q, want %q", i, r.Paths[i], want)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected file %q to exist", want)
		}
	}
}

func TestImageGenTool_RequiresPrompt(t *testing.T) {
	tool := NewImageGenTool(fakeImageGen{})
	if _, err := tool.Execute(context.Background(), map[string]any{}, nil); err == nil {
		t.Fatal("expected error when prompt missing")
	}
}

func TestImageGenTool_SurfacesProviderError(t *testing.T) {
	tool := NewImageGenTool(fakeImageGen{err: errors.New("not supported on this tier")})
	if _, err := tool.Execute(context.Background(), map[string]any{"prompt": "x"}, nil); err == nil {
		t.Fatal("expected provider error to surface")
	}
}
