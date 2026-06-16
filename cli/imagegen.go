package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arcaela/mini-cli/funcs"
	"github.com/arcaela/mini-cli/provider"
)

// =============================================================================
// generate_image — a tool wired only when the active provider can synthesise
// images (provider.ImageGenerator). It lives in cli, not funcs, because it
// needs the provider; funcs stays provider-agnostic.
// =============================================================================

type imageGenTool struct {
	gen provider.ImageGenerator
}

// NewImageGenTool returns a generate_image tool bound to a provider that can
// generate images. Register it into the tool registry at startup.
func NewImageGenTool(gen provider.ImageGenerator) funcs.Tool {
	return imageGenTool{gen: gen}
}

func (imageGenTool) Name() string        { return "generate_image" }
func (imageGenTool) Kind() funcs.Kind    { return funcs.KindBase }
func (imageGenTool) DependsOn() []string { return nil }
func (imageGenTool) Description() string {
	return "Generate image(s) from a text prompt with the active provider's image model and save them to disk. " +
		"Args: prompt (required), path (output file, optional), size (e.g. 1024x1024), count. Returns the saved file path(s)."
}

func (imageGenTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{"type": "string", "description": "Description of the image to generate."},
			"path":   map[string]any{"type": "string", "description": "Output file path. Defaults to mini-image-<timestamp>.png in the working directory; with count>1 an index is appended."},
			"size":   map[string]any{"type": "string", "description": "Image size such as 1024x1024 (provider default if omitted)."},
			"count":  map[string]any{"type": "integer", "description": "How many images to generate (default 1)."},
		},
		"required": []string{"prompt"},
	}
}

// RiskOf — writes file(s) to disk, so High.
func (imageGenTool) RiskOf(_ map[string]any) funcs.Risk { return funcs.RiskHigh }

type imageGenResult struct {
	Prompt string   `json:"prompt"`
	Paths  []string `json:"paths"`
	Count  int      `json:"count"`
}

func (t imageGenTool) Execute(ctx context.Context, args map[string]any, _ funcs.Caller) (any, error) {
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf(`arg "prompt" is required`)
	}
	size, _ := args["size"].(string)
	count := 1
	switch n := args["count"].(type) {
	case float64:
		if int(n) > 0 {
			count = int(n)
		}
	case int:
		if n > 0 {
			count = n
		}
	}
	basePath, _ := args["path"].(string)

	images, err := t.gen.GenerateImage(ctx, prompt, provider.ImageGenOptions{Size: size, Count: count})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(images))
	for i, img := range images {
		out := resolveImagePath(basePath, i, len(images), img.MimeType)
		if dir := filepath.Dir(out); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(out, img.Data, 0o644); err != nil {
			return nil, fmt.Errorf("save image: %w", err)
		}
		paths = append(paths, out)
	}
	return imageGenResult{Prompt: prompt, Paths: paths, Count: len(paths)}, nil
}

// resolveImagePath picks the output filename: the given base (or a timestamped
// default), inserting -N before the extension when there is more than one image.
func resolveImagePath(base string, idx, total int, mime string) string {
	ext := imageExt(mime)
	if base == "" {
		base = "mini-image-" + time.Now().UTC().Format("20060102T150405") + ext
	}
	if total <= 1 {
		return base
	}
	e := filepath.Ext(base)
	if e == "" {
		e = ext
	}
	return fmt.Sprintf("%s-%d%s", strings.TrimSuffix(base, filepath.Ext(base)), idx+1, e)
}

func imageExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}
