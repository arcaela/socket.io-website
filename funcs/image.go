package funcs

import (
	"path/filepath"
	"strings"
)

// =============================================================================
// Image support for tools.
//
// A tool whose result should be SEEN by the model (not just read as JSON text)
// returns a value implementing ImageProducer. The agent picks the images up and
// hands them to the provider, which renders them as native multimodal content.
//
// funcs stays provider-agnostic: it defines its own ToolImage type, and the
// agent converts it to provider.Image. No import cycle.
// =============================================================================

// ToolImage is raw image bytes plus their MIME type.
type ToolImage struct {
	MimeType string
	Data     []byte
}

// ImageProducer is implemented by tool results that carry model-visible images.
type ImageProducer interface {
	ToolImages() []ToolImage
}

// imageMIMEByExt maps known image extensions to their MIME type. Detection is
// extension-based on purpose: predictable, dependency-free, and the agent only
// ever reads files the user/model named explicitly.
var imageMIMEByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// imageMIME returns (mime, true) when path looks like a supported image.
func imageMIME(path string) (string, bool) {
	mime, ok := imageMIMEByExt[strings.ToLower(filepath.Ext(path))]
	return mime, ok
}

// maxImageBytes caps how large an image we will inline. base64 inflates ~33%,
// and most provider request limits sit around a handful of MiB.
const maxImageBytes = 5 << 20 // 5 MiB
