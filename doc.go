// Package mcpplane is an SDK-neutral MCP runtime library.
//
// The package provides the runtime machinery for hosting MCP servers —
// lifecycle management, transports, sessions, and dispatch — without
// coupling the core to any specific MCP SDK. SDK-specific adapters live
// in consumer or adapter packages, never in the core.
//
// The boundary is deliberate: this package must not import a particular
// MCP SDK, a terminal UI, or any product/deployment concern. Deployment
// and domain policies belong to the consuming application.
package mcpplane
