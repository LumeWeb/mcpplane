# mcpplane

[![Go Version](https://img.shields.io/badge/Go-1.26.0-blue)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Build Status](https://github.com/LumeWeb/mcpplane/actions/workflows/go.yml/badge.svg)](https://github.com/LumeWeb/mcpplane/actions/workflows/go.yml)

An SDK-neutral MCP runtime library.

`mcpplane` provides the runtime machinery for hosting MCP servers —
lifecycle management, transports, sessions, and dispatch — without
coupling the core to any specific MCP SDK. SDK-specific adapters live in
consumer or adapter packages, never in the core; deployment and domain
policies belong to the consuming application.

## Example usage

```go
rt := mcpplane.New(options...)

if err := rt.Start(ctx); err != nil {
    // ErrNotRunning / ErrClosed signal runtime state problems
}

defer rt.Close()
```

## License

MIT
