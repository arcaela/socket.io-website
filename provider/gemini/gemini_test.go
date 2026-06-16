package gemini

import (
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

func TestMessagesToContents_ToolResultWithImage(t *testing.T) {
	contents, _, err := messagesToContents([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			Name:   "read",
			Result: map[string]any{"kind": "image"},
			Images: []provider.Image{{MimeType: "image/png", Data: []byte{1, 2, 3}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || contents[0].Role != "user" {
		t.Fatalf("expected one user content, got %+v", contents)
	}
	parts := contents[0].Parts
	if len(parts) != 2 {
		t.Fatalf("expected functionResponse + inlineData parts, got %d", len(parts))
	}
	if parts[0].FunctionResponse == nil {
		t.Errorf("first part should be the functionResponse")
	}
	if parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != "AQID" {
		t.Errorf("bad inlineData part: %#v", parts[1].InlineData)
	}
}
