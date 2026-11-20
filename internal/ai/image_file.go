package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ImageFileExts are the file extensions (with the dot, lower case) accepted as image
// attachments.
var ImageFileExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true,
}

// IsImageFile reports whether path has an image attachment extension.
func IsImageFile(path string) bool {
	return ImageFileExts[strings.ToLower(filepath.Ext(path))]
}

// LoadImageFile reads an image attachment and returns it as vision input, normalized
// the same way for every client.
func LoadImageFile(path string) (ContentPart, error) {
	if !IsImageFile(path) {
		return ContentPart{}, fmt.Errorf("%s: not a supported image (png, jpg, jpeg, gif, webp, bmp)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ContentPart{}, err
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	ext, data = NormalizeImage(ext, data)
	return ImagePart(ext, data), nil
}

// ImageAttachmentNote is appended to a prompt that carries attached images, naming
// the files so the model can refer to them.
func ImageAttachmentNote(names []string) string {
	return "\n\n[note: the user attached image(s): " + strings.Join(names, "; ") +
		" — they are inlined above as vision input]"
}
