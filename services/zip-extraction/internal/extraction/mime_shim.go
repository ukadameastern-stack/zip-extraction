package extraction

import (
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// defaultDetectMime mirrors internal/storage.DetectMIME (BR-MIME-001). The
// duplication here is intentional to avoid a package import cycle (storage
// imports extraction). The two implementations MUST stay in lockstep — a
// PBT-05 oracle test in internal/storage_test verifies equivalence.
func defaultDetectMime(peek []byte, fileName string) string {
	sniffed := http.DetectContentType(peek)
	if sniffed != "application/octet-stream" {
		return sniffed
	}
	if ext := filepath.Ext(fileName); ext != "" {
		if t := mime.TypeByExtension(strings.ToLower(ext)); t != "" {
			return t
		}
	}
	return sniffed
}
