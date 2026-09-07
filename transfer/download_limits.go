package transfer

import (
	"errors"
	"fmt"
	"io"
)

// errDownloadTooLarge distinguishes an over-cap download stream error so the
// filedrop GET handler can map it to a 413 (rather than a generic 500) when the
// stream has not yet committed any body bytes. It wraps the cap so callers can
// also surface a human-readable cause.
type downloadTooLargeError struct {
	capBytes int64
}

func (e *downloadTooLargeError) Error() string {
	return fmt.Sprintf("download exceeds max_mcp_upload_size (%d bytes)", e.capBytes)
}

// IsDownloadTooLarge reports whether err is (or wraps) the over-cap download
// stream error minted by SizeLimitedWriter.
func IsDownloadTooLarge(err error) bool {
	var tl *downloadTooLargeError
	return errors.As(err, &tl)
}

// SizeLimitedWriter wraps an io.Writer with a hard byte cap. Unlike
// io.LimitWriter (which silently discards bytes past the cap — corrupting a
// download), it returns an error the moment a write would exceed maxBytes, so
// an over-limit stream fails loudly instead of landing a truncated file. A
// maxBytes <= 0 means "no limit".
type SizeLimitedWriter struct {
	w        io.Writer
	maxBytes int64
	written  int64
}

// NewSizeLimitedWriter wraps w with a hard maxBytes write cap.
func NewSizeLimitedWriter(w io.Writer, maxBytes int64) *SizeLimitedWriter {
	return &SizeLimitedWriter{w: w, maxBytes: maxBytes}
}

func (lw *SizeLimitedWriter) Write(p []byte) (int, error) {
	if lw.maxBytes > 0 && lw.written+int64(len(p)) > lw.maxBytes {
		return 0, &downloadTooLargeError{capBytes: lw.maxBytes}
	}
	n, err := lw.w.Write(p)
	lw.written += int64(n)
	return n, err
}
