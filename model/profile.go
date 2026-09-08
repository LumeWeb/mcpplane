package model

import (
	"net/http"
	"time"
)

// This file is the SDK-neutral platform-profile seam. In the source CLI these
// types lived in internal/mcp/hostenv, which also carries Pinner-specific
// surface gating (an opmesh/deployment concern). The subset below is host-
// agnostic and keeps this package free of CLI dependencies:
//
//   - Profile mirrors hostenv.PlatformProfile field-for-field minus the
//     Pinner-specific Surface field (which stays in the CLI). A consumer that
//     resolves a hostenv.PlatformProfile adapters over the shared fields.
//   - The Feature vocabulary and wire values are byte-identical to hostenv's
//     so profile resolution elsewhere never needs string translation.
//
// Everything else behaves exactly as the hostenv originals did.

// Feature is a named capability a host platform may or may not support.
// It functions like a caniuse entry: the target resolver checks whether the
// connected platform supports a feature to resolve which ToolTarget variant
// to materialize.
type Feature string

const (
	// FeatFileHostInput: host can build {download_url, file_id} file
	// references (OpenAI/ChatGPT runtime). Enables the top-level `file`
	// parameter on upload/download tools.
	FeatFileHostInput Feature = "file-host-input"

	// FeatSourcePath: co-located filesystem read. The server shares the
	// host filesystem, so source.mode=path works.
	FeatSourcePath Feature = "source-path"

	// FeatSourceMint: presigned HTTP PUT endpoint. The server has a
	// reachable HTTP mux, so source.mode=mint works.
	FeatSourceMint Feature = "source-mint"

	// FeatSourceURL: server-fetchable HTTPS URL relay. The server can
	// fetch a URL the host provides (OpenAI tunnel).
	FeatSourceURL Feature = "source-url"

	// FeatSourceData: RFC 2397 data: URI relay. The server can decode
	// inlined file bytes (OpenAI tunnel).
	FeatSourceData Feature = "source-data"

	// FeatXMcpFile: draft x-mcp-file metadata should be exposed on tools.
	FeatXMcpFile Feature = "x-mcp-file"

	// FeatSinkLocal: host-side disk write. The server writes bytes to
	// a local path for download tools.
	FeatSinkLocal Feature = "sink-local"

	// FeatSinkDrop: one-time HTTP GET filedrop. The server can serve a
	// transient download endpoint (a reachable HTTP mux on HTTP/tunnel, or
	// a spun-up local listener on stdio).
	FeatSinkDrop Feature = "sink-drop"

	// FeatMCPApps: client can render MCP Apps (ui:// resources with
	// MIME type text/html;profile=mcp-app).
	FeatMCPApps Feature = "mcp-apps-ui"

	// FeatElicitation: client supports form/URL elicitation
	// (input_required multi-round-trip).
	FeatElicitation Feature = "elicitation"

	// FeatRemoteAccess: server is reachable over HTTP from the client.
	// Implies the client is not co-located.
	FeatRemoteAccess Feature = "remote-access"

	// FeatCoLocated: server shares the host filesystem. The client
	// runs on the same machine as the MCP server.
	FeatCoLocated Feature = "co-located"
)

// FeatureSet is the set of features a platform profile supports.
type FeatureSet map[Feature]bool

// Has reports whether the feature set contains f.
func (fs FeatureSet) Has(f Feature) bool {
	return fs[f]
}

// HasAll reports whether the feature set contains every feature in req.
func (fs FeatureSet) HasAll(req FeatureSet) bool {
	for f := range req {
		if !fs[f] {
			return false
		}
	}
	return true
}

// Clone returns a shallow copy of the feature set. Callers that need to
// mutate a profile's features (e.g. to overlay runtime flags) MUST clone
// first — the FeatureSet in a static Profile is a shared map.
func (fs FeatureSet) Clone() FeatureSet {
	out := make(FeatureSet, len(fs))
	for f, ok := range fs {
		out[f] = ok
	}
	return out
}

// HostType identifies the connected MCP client platform. The values are kept
// byte-identical to the host vocabulary used by profile resolvers in hosting
// applications.
type HostType string

const (
	HostUnknown       HostType = "unknown"
	HostOpenAI        HostType = "openai"
	HostChatGPT       HostType = "chatgpt"
	HostGrok          HostType = "grok"
	HostOpenCode      HostType = "opencode"
	HostKilo          HostType = "kilo"
	HostKiro          HostType = "kiro"
	HostClaude        HostType = "claude"
	HostClaudeDesktop HostType = "claude-desktop"
	HostClaudeCode    HostType = "claude-code"

	// HostStdioApps is a synthetic host representing any co-located stdio
	// client that also renders MCP Apps UI. It is the alias target shared by
	// the concrete stdio hosts that present this surface (Claude Desktop,
	// Goose); it is never detected directly.
	HostStdioApps   HostType = "stdio-apps"
	HostAiderDesk   HostType = "aider-desk"
	HostDevin       HostType = "devin"
	HostCline       HostType = "cline"
	HostCodex       HostType = "codex"
	HostCopilotCLI  HostType = "copilot-cli"
	HostGoose       HostType = "goose"
	HostAntigravity HostType = "antigravity"
	HostKimi        HostType = "kimi"
	HostZed         HostType = "zed"
	HostFX          HostType = "fx"
	HostGeneric     HostType = "generic"
)

// TransportKind is the MCP transport the server runs under. It decides
// which file-input mechanism actually works: only one mechanism is real per
// transport, and the caller never picks it — registration and the resolver do.
type TransportKind string

const (
	// TransportStdio is co-located stdio/local mode.
	TransportStdio TransportKind = "stdio"
	// TransportHTTP is remote HTTP or a real tunnel with a reachable HTTP mux.
	TransportHTTP TransportKind = "http"
	// TransportOpenAI is the embedded OpenAI Secure MCP Tunnel: pure MCP
	// RPC with no reachable HTTP mux.
	TransportOpenAI TransportKind = "openai"
)

// AuthMethod describes how the client authenticated.
type AuthMethod string

const (
	AuthNone   AuthMethod = "none"
	AuthBearer AuthMethod = "bearer"
	AuthOAuth  AuthMethod = "oauth"
)

// ClientInfo carries the MCP clientInfoImplementation fields from the
// wire (initialize params or per-request _meta).
type ClientInfo struct {
	Name        string
	Version     string
	Title       string
	Description string
}

// TokenInfo carries the OAuth bearer token information extracted by the
// host's auth middleware.
type TokenInfo struct {
	Scopes     []string
	Expiration time.Time
	UserID     string
	Extra      map[string]any
}

// Profile is the resolved capability set for a specific host on
// a specific transport. It is the "browser profile" in the caniuse
// analogy: a static declaration of which features a HostType + Transport
// combination supports, overlaid with runtime wire signals.
//
// Unlike the hostenv original it has no Surface field: which operation
// domains a server exposes is a deployment/product concern that belongs to
// the consuming application, not this SDK-neutral model.
type Profile struct {
	HostType   HostType
	Transport  TransportKind
	AuthMethod AuthMethod
	Remote     bool
	Features   FeatureSet

	// Hosted reports whether this server is a hosted (embedded) assembly.
	// Like the surface vocabulary it is a server-construction-time deployment
	// property (set by the hosted construction path) rather than a wire
	// signal. Callers that need hosted-specific copy gate on it.
	Hosted bool

	// Raw wire signals, populated by the host's detector for runtime
	// introspection by tools that need them at call time.
	ClientInfo  *ClientInfo
	ProtocolVer string
	UserAgent   string
	Headers     http.Header
	TokenInfo   *TokenInfo
}

// Has reports whether the profile supports the given feature.
func (p Profile) Has(f Feature) bool {
	return p.Features.Has(f)
}

// IsTransport reports whether the profile's transport matches t.
func (p Profile) IsTransport(t TransportKind) bool {
	return p.Transport == t
}

// IsHost reports whether the profile's host type matches h.
func (p Profile) IsHost(h HostType) bool {
	return p.HostType == h
}

// CloneFeatures returns a shallow copy of this profile with a cloned
// FeatureSet. Callers that overlay runtime flags (e.g. setting
// FeatFileHostInput based on whether a relay handler is wired) MUST use
// this before mutating Features — the FeatureSet in a static profile
// is a shared map.
func (p Profile) CloneFeatures() Profile {
	p.Features = p.Features.Clone()
	return p
}
