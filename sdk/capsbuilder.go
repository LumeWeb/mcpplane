package sdk

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.lumeweb.com/canimcp"

	"go.lumeweb.com/mcpplane/model"
)

// RequestCapsOptions configures the shared per-request RequestCaps builder
// returned by NewRequestCapsBuilder.
type RequestCapsOptions struct {
	// Registry is the detector registry used to resolve the platform profile
	// from wire signals. When nil the shared default canimcp registry owned by
	// this package is used.
	Registry *canimcp.DetectorRegistry

	// CoLocated is true when the server was started in co-located stdio mode
	// (CLI stdio launch). Passed through to the detector's transport
	// resolution; the hosted flag overrides it.
	CoLocated bool

	// TunnelOpenAI is true when the server runs under the embedded OpenAI
	// tunnel transport. Passed through to the detector; the hosted flag
	// overrides it.
	TunnelOpenAI bool

	// Hosted stamps the resolved profile with the hosted deployment flag and
	// reports it to the detector as a remote HTTP server (co-located stdio and
	// the OpenAI tunnel never apply to a hosted embed).
	Hosted bool

	// DevSnapshot captures the raw wire snapshot (client capabilities +
	// initialize params) the dev_* introspection tools expose. When false the
	// hot path stays lean and both snapshot fields stay nil.
	DevSnapshot bool
}

// redactedPlaceholder replaces the value of every sensitive header or token
// claim so names survive (dev_host_env legitimately introspects names) while
// values never reach profiles, evidence, or dev-tool output.
const redactedPlaceholder = "[REDACTED]"

// sensitivePrefixes are case-insensitive header-name prefixes that denote
// request-routing metadata the client should not see echoed back.
var sensitivePrefixes = []string{"x-forwarded-"}

// NewRequestCapsBuilder returns the HandlerDeps.RequestCaps function: one
// shared implementation of the go-sdk request → wire signals → canimcp
// Detect → MCP-apps overlay → dev snapshot → model.RequestCaps/Profile
// projection pipeline. It is a pure function of the request; MCP is stateless
// and the capabilities arrive per-request, so the returned func is safe to
// invoke concurrently.
func NewRequestCapsBuilder(opts RequestCapsOptions) func(req *CallToolRequest) *model.RequestCaps {
	registry := opts.Registry
	if registry == nil {
		registry = canimcp.NewRegistry()
	}

	return func(req *CallToolRequest) *model.RequestCaps {
		rc := &model.RequestCaps{ProtocolVersion: req.ProtocolVersion()}
		if ci := req.ClientInfo(); ci != nil {
			rc.ClientName = ci.Name
			rc.ClientVersion = ci.Version
		}
		if cc := req.ClientCapabilities(); cc != nil {
			rc.UI = GetClientUICapability(cc.Extensions)
		}

		// Extract wire signals from req.GetExtra(): the go-sdk carries HTTP
		// headers and OAuth TokenInfo here over HTTP transports; on stdio
		// Extra is nil. The original header map is the live HTTP request's —
		// redact into a copy so secrets never enter evidence or profiles.
		var headers http.Header
		var tokenInfo *canimcp.TokenInfo
		if extra := req.GetExtra(); extra != nil {
			headers = RedactSensitiveHeaders(extra.Header)
			if extra.TokenInfo != nil {
				tokenInfo = &canimcp.TokenInfo{
					Scopes:     extra.TokenInfo.Scopes,
					Expiration: extra.TokenInfo.Expiration,
					UserID:     extra.TokenInfo.UserID,
					Extra:      RedactTokenClaims(extra.TokenInfo.Extra),
				}
			}
		}

		var clientInfo *canimcp.ClientInfo
		if ci := req.ClientInfo(); ci != nil {
			clientInfo = &canimcp.ClientInfo{
				Name:        ci.Name,
				Version:     ci.Version,
				Title:       ci.Title,
				Description: ci.Description,
			}
		}

		profile := registry.Detect(canimcp.Evidence{
			ClientInfo:      clientInfo,
			ProtocolVersion: req.ProtocolVersion(),
			UserAgent:       headers.Get("User-Agent"),
			Headers:         headers,
			TokenInfo:       tokenInfo,
			CoLocated:       opts.CoLocated,
			TunnelOpenAI:    opts.TunnelOpenAI,
		})

		// Safety net: a client advertising MCP Apps support on the wire
		// (io.modelcontextprotocol/ui with text/html;profile=mcp-app) but
		// without a matching static profile entry still must resolve the
		// mcp-apps-ui feature for tools that branch on it at call time.
		if rc.UI != nil && rc.UI.SupportsApps() && !profile.Has(canimcp.FeatMCPApps) {
			profile = profile.CloneFeatures()
			profile.Features[canimcp.FeatMCPApps] = true
		}

		shared := SharedProfileFromCore(profile)
		if opts.Hosted {
			// Hosted embeds are always remote HTTP servers behind a proxy:
			// neither co-located stdio nor the OpenAI tunnel wire applies.
			// Note canimcp core profiles carry no Hosted field — the hosted
			// deployment flag is a hosting-application concern stamped here.
			shared.Hosted = true
			shared.Remote = true
			shared.Transport = model.TransportHTTP
		}
		rc.Profile = &shared

		// When dev tools are enabled, capture the raw wire snapshot the dev_*
		// tools introspect. The go-sdk types are converted to SDK-neutral JSON
		// data so the model layer stays free of the protocol SDK. This is the
		// only signal that reliably describes a remote host across HTTP/OAuth
		// transports (the server's own process environment is unrelated).
		if opts.DevSnapshot {
			if cc := req.ClientCapabilities(); cc != nil {
				rc.Capabilities = ToJSONMap(cc)
			}
			if s := req.Session; s != nil {
				if ip := s.InitializeParams(); ip != nil {
					rc.InitializeParams = ToJSONMap(ip)
				}
			}
		}

		return rc
	}
}

// RedactSensitiveHeaders returns a copy of h with the values of sensitive
// headers replaced by a placeholder. Header names are preserved (including
// their original casing) because dev introspection legitimately reports which
// headers the client sent. Only Authorization, Cookie, Proxy-Authorization,
// and X-Forwarded-* are redacted: detectors key on other headers (e.g.
// OpenAI session/subject), so over-redacting would corrupt detection. The
// input header is never mutated.
//
// A nil header returns nil.
func RedactSensitiveHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		if isSensitiveHeader(name) {
			redacted := make([]string, len(values))
			for i := range values {
				redacted[i] = redactedPlaceholder
			}
			out[name] = redacted
			continue
		}
		out[name] = values
	}
	return out
}

// sensitiveTokenClaimFragments are case-insensitive key fragments of OAuth
// claims that carry raw credentials. Identity/audit claims (sub, aud, iss,
// scope, client_id, ...) are metadata and stay intact for dev introspection;
// only keys that can smuggle a credential (or values shaped like one) are
// redacted.
var sensitiveTokenClaimFragments = []string{
	"token",
	"secret",
	"password",
	"credential",
	"authorization",
	"jwt",
	"api_key",
	"apikey",
	"key",
	"signature",
	"signing",
	"cert",
	"hash",
}

// RedactTokenClaims returns a copy of the token's arbitrary claims map with
// credential-bearing entries replaced by a placeholder. Claim keys are
// preserved (dev introspection legitimately reports which claims the client
// presented) while values that look like raw credentials never reach
// evidence, profiles, or dev-tool output. Nested maps and slices are copied
// and redacted too, so no reference to client-owned data survives. The input
// map is never mutated.
//
// A nil map returns nil.
func RedactTokenClaims(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		if isSensitiveTokenClaim(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = redactClaimValue(v)
	}
	return out
}

// isSensitiveTokenClaim reports whether the claim key plausibly carries a raw
// credential rather than identity or audit metadata.
func isSensitiveTokenClaim(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range sensitiveTokenClaimFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

// redactClaimValue copies v, recursing into nested maps and slices (clients
// can nest credentials under innocuously-named claims) and redacting string
// values shaped like raw credentials. It never returns a reference to a
// caller-owned map or slice. Non-string scalars pass through by value.
func redactClaimValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, inner := range v {
			if isSensitiveTokenClaim(k) {
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = redactClaimValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = redactClaimValue(inner)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(v))
		for k, inner := range v {
			if isSensitiveTokenClaim(k) || looksLikeCredential(inner) {
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = inner
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, inner := range v {
			if looksLikeCredential(inner) {
				out[i] = redactedPlaceholder
				continue
			}
			out[i] = inner
		}
		return out
	case map[any]any:
		out := make(map[any]any, len(v))
		for k, inner := range v {
			if ks, ok := k.(string); ok && isSensitiveTokenClaim(ks) {
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = redactClaimValue(inner)
		}
		return out
	case string:
		if looksLikeCredential(v) {
			return redactedPlaceholder
		}
		return v
	default:
		return v
	}
}

// looksLikeCredential reports whether a string value is shaped like a raw
// credential regardless of its claim name: a compact JWS/JWT (three dot-
// separated base64url segments), a PEM block, or a long opaque base64url
// token. Identity and audit values (sub, aud, scope, timestamps, short ids)
// never match, so they pass through for dev introspection.
func looksLikeCredential(s string) bool {
	if strings.HasPrefix(s, "-----BEGIN") {
		return true
	}
	if strings.Count(s, ".") == 2 && len(s) > 40 && strings.Trim(s, "._-") != "" {
		// A JWS compact serialization: header.payload.signature in
		// base64url, no other dot-delimited text this long.
		for _, part := range s {
			if part != '.' && !isBase64URLRune(part) {
				return false
			}
		}
		return true
	}
	if len(s) >= 64 && strings.IndexFunc(s, func(r rune) bool { return !isBase64URLRune(r) }) == -1 {
		return true
	}
	return false
}

// isBase64URLRune reports whether r is valid in base64url (RFC 4648 §5).
func isBase64URLRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '='
}

// isSensitiveHeader reports whether the header name carries a secret value.
func isSensitiveHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	switch canonical {
	case "Authorization", "Cookie", "Proxy-Authorization":
		return true
	}
	lower := strings.ToLower(name)
	for _, prefix := range sensitivePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// SharedProfileFromCore adapts a canimcp profile to the SDK-neutral
// model.Profile. The vocabularies are byte-identical string sets; only the
// named types differ, so the projection is an infallible field-by-field copy.
// (mcpplane's model package deliberately does not alias canimcp's types — the
// dependency must stay one-directional: adapters depend on model, not model on
// adapter libraries.)
func SharedProfileFromCore(p canimcp.Profile) model.Profile {
	var clientInfo *model.ClientInfo
	if p.ClientInfo != nil {
		clientInfo = &model.ClientInfo{
			Name:        p.ClientInfo.Name,
			Version:     p.ClientInfo.Version,
			Title:       p.ClientInfo.Title,
			Description: p.ClientInfo.Description,
		}
	}
	var tokenInfo *model.TokenInfo
	if p.TokenInfo != nil {
		tokenInfo = &model.TokenInfo{
			Scopes:     p.TokenInfo.Scopes,
			Expiration: p.TokenInfo.Expiration,
			UserID:     p.TokenInfo.UserID,
			Extra:      RedactTokenClaims(p.TokenInfo.Extra),
		}
	}

	features := make(model.FeatureSet, len(p.Features))
	for f, ok := range p.Features {
		features[model.Feature(f)] = ok
	}
	return model.Profile{
		HostType:    model.HostType(p.HostType),
		Transport:   model.TransportKind(p.Transport),
		AuthMethod:  model.AuthMethod(p.AuthMethod),
		Remote:      p.Remote,
		Features:    features,
		ClientInfo:  clientInfo,
		ProtocolVer: p.ProtocolVer,
		UserAgent:   p.UserAgent,
		Headers:     p.Headers,
		TokenInfo:   tokenInfo,
	}
}

// GetClientUICapability reads the typed MCP Apps capability from a client's
// advertised `extensions` (map of extension id -> settings). It returns nil if
// the client did not advertise MCP Apps. The apps package re-exports this
// helper so existing callers keep compiling.
func GetClientUICapability(extensions map[string]any) *model.ClientUICapabilities {
	raw, ok := extensions[UICapabilityID]
	if !ok || raw == nil {
		return nil
	}
	// The extension setting is a plain object (not an array/scalar). Decode it
	// typed rather than casting fields by hand.
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var parsed struct {
		MIMETypes []string `json:"mimeTypes"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	if parsed.MIMETypes == nil {
		return &model.ClientUICapabilities{}
	}
	return &model.ClientUICapabilities{MIMETypes: parsed.MIMETypes}
}

// ToJSONMap converts a go-sdk-typed value into a plain JSON map so the
// SDK-neutral model layer can carry it without importing the protocol SDK.
// It returns nil when the value cannot be marshaled (defensive; the SDK
// structs used here always serialize).
func ToJSONMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}
