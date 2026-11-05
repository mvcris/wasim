package cloudapi

import (
	"fmt"
	"strings"
)

// Media kinds an inbound message can carry in this version.
const (
	MediaImage    = "image"
	MediaDocument = "document"
)

const (
	megabyte = 1024 * 1024

	maxImageBytes    = 5 * megabyte
	maxDocumentBytes = 100 * megabyte
)

var imageMimeTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

var documentMimeTypes = map[string]struct{}{
	"text/plain":               {},
	"application/pdf":          {},
	"application/vnd.ms-excel": {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": {},
	"application/msword": {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {},
	"application/vnd.ms-powerpoint":                                             {},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {},
}

// MediaRule is what the platform accepts for one MIME type.
type MediaRule struct {
	Kind     string
	MaxBytes int64
}

// LookupMediaRule returns the rule for a MIME type, ignoring parameters such as
// charset. An error means the platform would not carry the file at all.
func LookupMediaRule(mimeType string) (MediaRule, error) {
	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	if _, ok := imageMimeTypes[base]; ok {
		return MediaRule{Kind: MediaImage, MaxBytes: maxImageBytes}, nil
	}
	if _, ok := documentMimeTypes[base]; ok {
		return MediaRule{Kind: MediaDocument, MaxBytes: maxDocumentBytes}, nil
	}
	return MediaRule{}, fmt.Errorf("unsupported media type %q", mimeType)
}
