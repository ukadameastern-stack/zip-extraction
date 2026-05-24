package storage

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// DetectMIME implements BR-MIME-001 hybrid detection:
//  1. Try content-sniff via net/http.DetectContentType(peek).
//  2. If the sniff result is "application/octet-stream" (i.e., sniff was
//     uninformative) AND the extension lookup yields a non-empty type, return
//     the extension-derived type.
//  3. Otherwise return the sniff result.
//
// The function never returns "" — at worst it returns "application/octet-stream".
// Exported for PBT-05 oracle property in tests.
func DetectMIME(peek []byte, fileName string) string {
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

// PeekedReader pairs a peeked header byte slice with a rebuilt reader so the
// caller can stream the full body without a second read pass.
type PeekedReader struct {
	Peek    []byte
	Rebuilt io.Reader
}

// Peek reads up to n bytes from r without advancing the read position. The
// Rebuilt reader reproduces the full byte sequence (peek + remainder) so the
// caller can stream onward — it is the bufio.Reader itself, since Peek leaves
// the peeked bytes in the buffer for subsequent reads.
func Peek(r io.Reader, n int) (PeekedReader, error) {
	br := bufio.NewReaderSize(r, n)
	peek, err := br.Peek(n)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return PeekedReader{}, err
	}
	// peek may be shorter than n on small streams; that's fine.
	peekCopy := make([]byte, len(peek))
	copy(peekCopy, peek)
	return PeekedReader{Peek: peekCopy, Rebuilt: br}, nil
}

// unused references the bytes package kept for potential future helpers.
var _ = bytes.NewReader
