package slipsheet

import (
	"bytes"
	"io"
)

// bytesReader wraps a byte slice as an io.Reader without copying.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
