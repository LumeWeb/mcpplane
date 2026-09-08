# AGENTS.md

This file provides development guidelines and architectural documentation for
the mcpplane project.

## Common Commands

### Building
```bash
# Build all packages
go build -v ./...
```

### Testing
```bash
# Run all tests with race detection and coverage
go test -v -race -coverprofile=coverage.out -covermode=atomic ./...

# Run tests for the root package only
go test -v -race .

# View coverage report
go tool cover -func=coverage.out
```

### Mock Generation

```bash
# Generate mocks for interfaces (uses .mockery.yaml; mockery is
# pre-installed at $HOME/go/bin/mockery; never reinstall it)
mockery
```

### Dependency Management
```bash
# Download dependencies
go mod download

# Verify dependencies
go mod verify

# Tidy dependencies
go mod tidy
```

## Project Overview

mcpplane is an SDK-neutral MCP runtime library. It provides the runtime
machinery for hosting MCP servers — lifecycle management, transports,
sessions, and dispatch — without coupling the core to any specific MCP SDK.

## Architecture

### Package Structure
- **Module path**: `go.lumeweb.com/mcpplane`
- **Root package** (`mcpplane`): all functionality as a flat package;
  `doc.go` holds the package documentation
- **Generated mocks**: `mocks/` (mockery, testify templates)

### Design
- **SDK neutrality is the boundary**: the core package must not import a
  particular MCP SDK, a terminal UI, or any product/deployment concern.
  SDK-specific adapters belong in an adapter package, never in the core.
- **Sentinel errors**: runtime state is signaled through package-level
  sentinel errors in `errors.go` (`ErrClosed`, `ErrNotRunning`,
  `ErrNotImplemented`); wrap them with `%w` for context.

### Testing Conventions
- Tests are colocated next to source (`*_test.go`)
- Do not add a README/board copyright header to source files; attribution
  lives only in the LICENSE file
- Generated mocks live in `mocks/` (mockery, testify templates)
