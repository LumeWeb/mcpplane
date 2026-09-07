package mcpplane

import "errors"

// ErrNotImplemented is returned when a requested capability or operation
// has no runtime implementation yet.
var ErrNotImplemented = errors.New("mcpplane: not implemented")

// ErrClosed is returned when an operation is attempted on a runtime that
// has already been shut down.
var ErrClosed = errors.New("mcpplane: runtime closed")

// ErrNotRunning is returned when an operation requires the runtime to be
// started, but it has not been started yet.
var ErrNotRunning = errors.New("mcpplane: runtime not running")
