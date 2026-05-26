package extraction

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// openZip opens the archive at path and returns the zip.Reader plus the
// pre-check ArchiveMetadata. Rejects encrypted, multi-disk, and Deflate64
// archives per FR-3.6 with *UnsupportedFeatureError.
func openZip(path string, size int64) (*zip.Reader, ArchiveMetadata, error) {
	if size <= 0 {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, ArchiveMetadata{}, fmt.Errorf("stat: %w", err)
		}
		size = fi.Size()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ArchiveMetadata{}, fmt.Errorf("open: %w", err)
	}
	// Caller does NOT close f directly — the returned *zip.Reader retains it.
	// Use a finalizer via Close on the underlying File only when openZip returns
	// an error. On success, caller owns the file handle via defer at call site.
	zr, err := zip.NewReader(f, size)
	if err != nil {
		_ = f.Close()
		return nil, ArchiveMetadata{}, fmt.Errorf("parseZip: %w", err)
	}

	meta := ArchiveMetadata{
		EntryCount:      len(zr.File),
		EntryDataRanges: make([]EntryDataRange, 0, len(zr.File)),
	}
	const deflate64 = 9
	for i, fh := range zr.File {
		meta.TotalCompressedBytes += int64(fh.CompressedSize64)
		// math.MaxUint32 is the ZIP32 → ZIP64 transition marker for files in the
		// central directory; we don't reject ZIP64 (per FR-3.6 it's supported).
		if fh.UncompressedSize64 > math.MaxUint32 {
			meta.ZIP64 = true
		}
		if fh.UncompressedSize64 > 0 {
			meta.TotalDeclaredUncompressedBytes += int64(fh.UncompressedSize64)
		}
		if (fh.Flags & 0x1) != 0 {
			meta.Encrypted = true
		}
		if fh.Method == deflate64 {
			meta.HasDeflate64Entries = true
		}
		// Compressed-data interval for BR-BOMB-009 overlap detection. DataOffset
		// resolves the local-file-header + filename + extra, giving the actual
		// compressed-bytes start. Best-effort: a DataOffset error (rare — implies
		// a malformed local header) just skips this entry's range; the rest still
		// get checked.
		if dataOff, derr := fh.DataOffset(); derr == nil {
			meta.EntryDataRanges = append(meta.EntryDataRanges, EntryDataRange{
				EntryIndex: i,
				Start:      dataOff,
				End:        dataOff + int64(fh.CompressedSize64),
			})
		}
	}

	if meta.Encrypted {
		_ = f.Close()
		return nil, ArchiveMetadata{}, &UnsupportedFeatureError{Feature: FeatureEncryptedZIP}
	}
	if meta.HasDeflate64Entries {
		_ = f.Close()
		return nil, ArchiveMetadata{}, &UnsupportedFeatureError{Feature: FeatureDeflate64}
	}
	// archive/zip does not surface multi-disk metadata directly; the stdlib
	// returns ErrFormat on multi-disk archives during NewReader, so by the time
	// we reach this point a multi-disk archive has already been rejected as a
	// corrupt-zip parse error.

	return zr, meta, nil
}

// spillToTemp materialises the archive body to a temp file so archive/zip can
// random-access. The file is removed by the caller's `defer os.Remove(path)`.
// Errors are wrapped with context for diagnostic logging.
func spillToTemp(body io.Reader, execID string) (string, error) {
	f, err := os.CreateTemp("", "zip-extraction-"+execID+"-*.zip")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, body); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// peekReader is a tiny shim around storage.Peek to keep the extraction package's
// dependency surface narrow. Implemented inline to avoid a package import cycle
// (storage already imports extraction). See internal/storage.Peek for the
// equivalent public helper.
type peekedReader struct {
	Peek    []byte
	Rebuilt io.Reader
}

func peekReader(r io.Reader, n int) (peekedReader, error) {
	buf := make([]byte, n)
	read, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return peekedReader{}, err
	}
	peek := buf[:read]
	// Re-prepend the consumed bytes to the original reader so downstream can
	// stream the full content.
	return peekedReader{Peek: peek, Rebuilt: io.MultiReader(byteReader(peek), r)}, nil
}

type byteReader []byte

func (b byteReader) Read(p []byte) (int, error) {
	if len(b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b)
	if n == len(b) {
		return n, io.EOF
	}
	return n, nil
}

// detectMimeShim mirrors internal/storage.DetectMIME without the package import.
// Kept in lockstep with internal/storage/mime.go.
func detectMimeShim(peek []byte, fileName string) string {
	return detectMimeFn(peek, fileName)
}

// detectMimeFn is overridable in tests; default delegates to net/http.
var detectMimeFn = defaultDetectMime
