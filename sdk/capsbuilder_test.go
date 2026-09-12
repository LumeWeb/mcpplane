package sdk

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.lumeweb.com/canimcp"
	"go.lumeweb.com/mcpplane/model"
)

// callToolReqWithMeta builds a minimal go-sdk call-tool request carrying
// per-request meta and HTTP wire extras, mirroring how the streamable
// transport materializes a remoted call.
func callToolReqWithMeta(t *testing.T, meta map[string]any, extra *mcp.RequestExtra) *CallToolRequest {
	t.Helper()
	return &CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Meta: mcp.Meta(meta)},
		Extra:  extra,
	}
}

// uiClientMeta builds a request _meta map for a client advertising MCP Apps
// support (io.modelcontextprotocol/ui with the mcp-app MIME type).
func uiClientMeta() map[string]any {
	return map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientInfo:         map[string]any{"name": "test-host", "version": "1.0.0"},
		mcp.MetaKeyClientCapabilities: map[string]any{"extensions": map[string]any{UICapabilityID: map[string]any{"mimeTypes": []any{MCPAppsMIMEType, "text/plain"}}}},
	}
}

// textClientMeta builds a request _meta map for a client with no optional
// capabilities (a text-only host with no MCP Apps support).
func textClientMeta() map[string]any {
	return map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientInfo:         map[string]any{"name": "text-agent", "version": "0.9.0"},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}
}

// TestRequestCapsResolvesHostedHTTPProfile locks the hosted per-request
// capability view: profile detection over the canimcp registry must resolve
// the remote HTTP generic profile for an unidentified client, stamped with the
// hosted deployment flag.
func TestRequestCapsResolvesHostedHTTPProfile(t *testing.T) {
	build := NewRequestCapsBuilder(RequestCapsOptions{Hosted: true})

	req := callToolReqWithMeta(t, map[string]any{
		mcp.MetaKeyProtocolVersion: "2025-06-18",
		mcp.MetaKeyClientInfo: map[string]any{
			"name": "unittest-host", "version": "v1",
		},
	}, &mcp.RequestExtra{
		Header: http.Header{"User-Agent": []string{"unittest-agent/1.0"}},
	})

	rc := build(req)
	require.NotNil(t, rc, "request caps must resolve")
	assert.Equal(t, "2025-06-18", rc.ProtocolVersion)
	assert.Equal(t, "unittest-host", rc.ClientName)
	require.NotNil(t, rc.Profile, "profile must resolve")
	assert.Equal(t, model.TransportHTTP, rc.Profile.Transport, "hosted embeds resolve the HTTP transport")
	assert.True(t, rc.Profile.Hosted, "profile must carry the hosted deployment flag")
	assert.True(t, rc.Profile.Remote, "hosted embeds are always remote")
	assert.Equal(t, "unittest-agent/1.0", rc.Profile.UserAgent, "wire User-Agent must overlay the profile")
	require.NotNil(t, rc.Profile.ClientInfo)
	assert.Equal(t, "unittest-host", rc.Profile.ClientInfo.Name)
	assert.Empty(t, rc.Capabilities, "the raw wire snapshot must stay absent with dev tools off")
}

// TestRequestCapsTransportResolution locks the non-hosted transport
// resolution: the detector must resolve the transport from the launch flags
// (co-located stdio vs OpenAI tunnel vs plain HTTP), and hosted stamping must
// be absent.
func TestRequestCapsTransportResolution(t *testing.T) {
	req := func() *CallToolRequest {
		return callToolReqWithMeta(t, textClientMeta(), &mcp.RequestExtra{
			Header: http.Header{"User-Agent": []string{"unittest-agent/1.0"}},
		})
	}

	// Plain HTTP (no flags): remote HTTP transport.
	rc := NewRequestCapsBuilder(RequestCapsOptions{})(req())
	require.NotNil(t, rc.Profile)
	assert.Equal(t, model.TransportHTTP, rc.Profile.Transport)
	assert.False(t, rc.Profile.Hosted, "non-hosted builder must not stamp the hosted flag")

	// Co-located stdio.
	rc = NewRequestCapsBuilder(RequestCapsOptions{CoLocated: true})(req())
	require.NotNil(t, rc.Profile)
	assert.Equal(t, model.TransportStdio, rc.Profile.Transport)
	assert.False(t, rc.Profile.Hosted)

	// OpenAI tunnel.
	rc = NewRequestCapsBuilder(RequestCapsOptions{TunnelOpenAI: true})(req())
	require.NotNil(t, rc.Profile)
	assert.Equal(t, model.TransportOpenAI, rc.Profile.Transport)
	assert.False(t, rc.Profile.Hosted)
}

// TestRequestCapsMCPAppsOverlay locks the safety net: a client advertising
// MCP Apps support on the wire but with no matching static profile entry
// (generic HTTP host) still resolves the mcp-apps-ui feature, and the overlay
// clones the static feature map rather than mutating the shared registry map.
func TestRequestCapsMCPAppsOverlay(t *testing.T) {
	registry := canimcp.NewRegistry()
	build := NewRequestCapsBuilder(RequestCapsOptions{Registry: registry})

	req := callToolReqWithMeta(t, uiClientMeta(), &mcp.RequestExtra{
		Header: http.Header{"User-Agent": []string{"curl/8.0"}},
	})

	rc := build(req)
	require.NotNil(t, rc.Profile)
	require.NotNil(t, rc.UI, "UI caps must resolve from the wire extension")
	require.True(t, rc.SupportsApps())
	assert.True(t, rc.Profile.Has(model.FeatMCPApps), "wire-advertised MCP Apps must overlay the feature")

	// The overlay must not have leaked into the static profile the registry
	// hands out: a text-only client on the same host still lacks the feature.
	text := build(callToolReqWithMeta(t, textClientMeta(), &mcp.RequestExtra{
		Header: http.Header{"User-Agent": []string{"curl/8.0"}},
	}))
	require.NotNil(t, text.Profile)
	assert.False(t, text.Profile.Has(model.FeatMCPApps))

	// A text-only client must not resolve UI caps at all.
	assert.Nil(t, text.UI)
	assert.False(t, text.SupportsApps())
}

// TestRequestCapsNilSafe locks the no-meta resilience: a request with no _meta
// and no session yields empty-but-non-nil caps.
func TestRequestCapsNilSafe(t *testing.T) {
	var rc *model.RequestCaps
	assert.False(t, rc.SupportsApps(), "nil RequestCaps should not support apps")

	req := &CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "x"}}
	got := NewRequestCapsBuilder(RequestCapsOptions{})(req)
	require.NotNil(t, got, "expected non-nil RequestCaps even with no meta")
	assert.Empty(t, got.ProtocolVersion)
	assert.Empty(t, got.ClientName)
	assert.Nil(t, got.UI)
}

// TestRequestCapsDevSnapshot locks the dev-tools wire snapshot: the raw
// client capabilities and initialize params are captured only when dev tools
// are on.
func TestRequestCapsDevSnapshot(t *testing.T) {
	meta := map[string]any{
		mcp.MetaKeyClientCapabilities: map[string]any{
			"roots": map[string]any{"listChanged": true},
		},
	}

	off := NewRequestCapsBuilder(RequestCapsOptions{})(callToolReqWithMeta(t, meta, nil))
	assert.Nil(t, off.Capabilities, "dev tools off: no capabilities snapshot")
	assert.Nil(t, off.InitializeParams)

	on := NewRequestCapsBuilder(RequestCapsOptions{DevSnapshot: true})(callToolReqWithMeta(t, meta, nil))
	assert.NotNil(t, on.Capabilities, "dev tools on: capabilities snapshot captured")
	assert.Contains(t, on.Capabilities, "roots")
}

func TestRequestCapsRedactsSensitiveHeaders(t *testing.T) {
	req := callToolReqWithMeta(t, textClientMeta(), &mcp.RequestExtra{
		Header: http.Header{
			"Authorization":       {"Bearer dummy-value"},
			"Cookie":              {"session=dummy-value"},
			"Proxy-Authorization": {"Basic dummy-value"},
			"X-Forwarded-For":     {"203.0.113.7"},
			"X-Forwarded-Proto":   {"https"},
			"User-Agent":          {"curl/8.0"},
		},
	})

	rc := NewRequestCapsBuilder(RequestCapsOptions{Hosted: true})(req)
	require.NotNil(t, rc.Profile)
	require.NotNil(t, rc.Profile.Headers)

	h := rc.Profile.Headers
	assert.Equal(t, []string{redactedPlaceholder}, h["Authorization"], "Authorization value must be redacted")
	assert.Equal(t, []string{redactedPlaceholder}, h["Cookie"], "Cookie value must be redacted")
	assert.Equal(t, []string{redactedPlaceholder}, h["Proxy-Authorization"], "Proxy-Authorization value must be redacted")
	assert.Equal(t, []string{redactedPlaceholder}, h["X-Forwarded-For"], "X-Forwarded-For value must be redacted")
	assert.Equal(t, []string{redactedPlaceholder}, h["X-Forwarded-Proto"], "X-Forwarded-Proto value must be redacted")
	// Names are preserved (dev_host_env legitimately introspects names).
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Forwarded-For", "X-Forwarded-Proto"} {
		assert.Contains(t, h, name, "header name %s must be preserved", name)
	}
	assert.Equal(t, []string{"curl/8.0"}, h["User-Agent"], "non-sensitive headers pass through")

	// The live request header must be left intact; redaction happens in a
	// copy.
	assert.Equal(t, "Bearer dummy-value", req.Extra.Header.Get("Authorization"))
	assert.Equal(t, "session=dummy-value", req.Extra.Header.Get("Cookie"))
	assert.Equal(t, "Basic dummy-value", req.Extra.Header.Get("Proxy-Authorization"))
	assert.Equal(t, "203.0.113.7", req.Extra.Header.Get("X-Forwarded-For"))
	assert.Equal(t, "https", req.Extra.Header.Get("X-Forwarded-Proto"))
}

func TestRequestCapsRedactsTokenClaims(t *testing.T) {
	req := callToolReqWithMeta(t, textClientMeta(), &mcp.RequestExtra{
		TokenInfo: &auth.TokenInfo{
			Scopes:     []string{"vault:read"},
			Expiration: time.Unix(0, 0).UTC(),
			UserID:     "user-42",
			Extra: map[string]any{
				"access_token": "raw-jwt-value",
				"id_token":     "another-jwt-value",
				"proxy_secret": "dummy-value",
				// Identity/audit claims are metadata, not credentials.
				"sub":   "user-42",
				"aud":   []any{"api"},
				"scope": "vault:read",
			},
		},
	})

	rc := NewRequestCapsBuilder(RequestCapsOptions{Hosted: true})(req)
	require.NotNil(t, rc.Profile)
	require.NotNil(t, rc.Profile.TokenInfo)

	claims := rc.Profile.TokenInfo.Extra
	assert.Equal(t, redactedPlaceholder, claims["access_token"], "raw token claims must be redacted")
	assert.Equal(t, redactedPlaceholder, claims["id_token"])
	assert.Equal(t, redactedPlaceholder, claims["proxy_secret"])
	assert.Equal(t, "user-42", claims["sub"], "identity claims stay intact")
	assert.Equal(t, []any{"api"}, claims["aud"])
	assert.Equal(t, "vault:read", claims["scope"])
	assert.Equal(t, "user-42", rc.Profile.TokenInfo.UserID, "UserID passes through unredacted")

	// The live token info must be left intact.
	assert.Equal(t, "raw-jwt-value", req.Extra.TokenInfo.Extra["access_token"])
}

func TestRedactTokenClaims(t *testing.T) {
	t.Run("nil map stays nil", func(t *testing.T) {
		assert.Nil(t, RedactTokenClaims(nil))
	})

	// Every denylist fragment must trigger redaction, whatever key embeds it.
	t.Run("every sensitive fragment redacts", func(t *testing.T) {
		for _, fragment := range sensitiveTokenClaimFragments {
			key := "x_" + fragment + "_y"
			out := RedactTokenClaims(map[string]any{key: "value"})
			assert.Equal(t, redactedPlaceholder, out[key], "claim %s must be redacted", key)
		}
	})

	t.Run("identity claims pass through", func(t *testing.T) {
		out := RedactTokenClaims(map[string]any{
			"sub":       "user-42",
			"aud":       "api",
			"scope":     "vault:read",
			"iss":       "https://sso.example",
			"client_id": "public-app-id",
		})
		assert.Equal(t, "user-42", out["sub"])
		assert.Equal(t, "api", out["aud"])
		assert.Equal(t, "vault:read", out["scope"])
		assert.Equal(t, "https://sso.example", out["iss"])
		assert.EqualValues(t, "public-app-id", out["client_id"])
	})

	t.Run("credential-shaped values redacted under innocuous keys", func(t *testing.T) {
		jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.hunterS2r1Fc2t8dbXJh9P1L2XaKvYQ"
		pem := "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"
		longOpaque := strings.Repeat("a8Hq", 20) // 80 chars of base64url
		out := RedactTokenClaims(map[string]any{
			"sso_ticket":   jwt,
			"upload_pem":   pem,
			"handoff_blob": longOpaque,
		})
		assert.Equal(t, redactedPlaceholder, out["sso_ticket"], "JWT-shaped values must be redacted")
		assert.Equal(t, redactedPlaceholder, out["upload_pem"], "PEM blocks must be redacted")
		assert.Equal(t, redactedPlaceholder, out["handoff_blob"], "long opaque base64url values must be redacted")

		assert.Equal(t, "a.b.c", RedactTokenClaims(map[string]any{"sub_note": "a.b.c"})["sub_note"],
			"dot-delimited text that is not a JWS must pass through")
	})

	t.Run("nested maps and slices are copied and redacted", func(t *testing.T) {
		in := map[string]any{
			"workspace": map[string]any{
				"access_token": "inner-value",
				"name":         "ok",
				"deep":         map[string]any{"refresh_token": "inner2"},
			},
			"history": []any{map[string]any{"api_key": "k", "sub": "u"}, "audit-claim"},
		}
		out := RedactTokenClaims(in)

		ws := out["workspace"].(map[string]any)
		assert.Equal(t, redactedPlaceholder, ws["access_token"])
		assert.Equal(t, "ok", ws["name"])
		assert.Equal(t, redactedPlaceholder, ws["deep"].(map[string]any)["refresh_token"])

		entry := out["history"].([]any)[0].(map[string]any)
		assert.Equal(t, redactedPlaceholder, entry["api_key"])
		assert.Equal(t, "u", entry["sub"])

		// Container types verifiers commonly build natively are covered too.
		native := RedactTokenClaims(map[string]any{
			"strings_map": map[string]string{"signing_key": "native", "region": "eu"},
			"string_list": []string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.hunterS2r1Fc2t8dbXJh9P1L2XaKvYQ", "plain"},
			"any_map":     map[any]any{"token": "any-map-cred", "kind": "meta"},
		})
		sm := native["strings_map"].(map[string]string)
		assert.Equal(t, redactedPlaceholder, sm["signing_key"])
		assert.Equal(t, "eu", sm["region"])
		sl := native["string_list"].([]string)
		assert.Equal(t, redactedPlaceholder, sl[0], "JWT-shaped values redact inside []string")
		assert.Equal(t, "plain", sl[1])
		am := native["any_map"].(map[any]any)
		assert.Equal(t, redactedPlaceholder, am["token"])
		assert.Equal(t, "meta", am["kind"])

		// No reference to client-owned containers survives.
		inner := in["workspace"].(map[string]any)
		assert.Equal(t, "inner-value", inner["access_token"], "input must be untouched")
		inner["access_token"] = "mutated"
		assert.Equal(t, redactedPlaceholder, out["workspace"].(map[string]any)["access_token"],
			"nested maps must be deep copies, not shared references")
	})

	t.Run("input map untouched", func(t *testing.T) {
		in := map[string]any{"access_token": "raw"}
		_ = RedactTokenClaims(in)
		assert.Equal(t, "raw", in["access_token"])
	})
}

func TestRedactSensitiveHeaders(t *testing.T) {
	t.Run("nil header stays nil", func(t *testing.T) {
		assert.Nil(t, RedactSensitiveHeaders(nil))
	})

	t.Run("values redacted, names and casing preserved", func(t *testing.T) {
		in := http.Header{
			"Authorization":       {"Bearer x"},
			"authorization":       {"Bearer y"},
			"Cookie":              {"a=b"},
			"Proxy-Authorization": {"Basic c"},
			"X-Forwarded-Host":    {"evil.example"},
			"x-forwarded-proto":   {"http"},
			"Mcp-Session-Id":      {"keep-me"},
		}
		out := RedactSensitiveHeaders(in)
		assert.Equal(t, []string{redactedPlaceholder}, out["Authorization"])
		assert.Equal(t, []string{redactedPlaceholder}, out["authorization"])
		assert.Equal(t, []string{redactedPlaceholder}, out["Cookie"])
		assert.Equal(t, []string{redactedPlaceholder}, out["Proxy-Authorization"])
		assert.Equal(t, []string{redactedPlaceholder}, out["X-Forwarded-Host"])
		assert.Equal(t, []string{redactedPlaceholder}, out["x-forwarded-proto"])
		assert.Equal(t, []string{"keep-me"}, out["Mcp-Session-Id"])
	})

	t.Run("input map untouched", func(t *testing.T) {
		in := http.Header{"Authorization": {"Bearer x"}}
		_ = RedactSensitiveHeaders(in)
		assert.Equal(t, []string{"Bearer x"}, in["Authorization"])
	})
}

func TestSharedProfileFromCore(t *testing.T) {
	in := canimcp.Profile{
		HostType:    canimcp.HostGrok,
		Transport:   canimcp.TransportHTTP,
		AuthMethod:  canimcp.AuthOAuth,
		Remote:      true,
		Features:    canimcp.FeatureSet{canimcp.FeatMCPApps: true},
		ClientInfo:  &canimcp.ClientInfo{Name: "ci", Version: "1", Title: "CI", Description: "ci client"},
		ProtocolVer: "2025-06-18",
		UserAgent:   "grok/1",
		Headers:     http.Header{"User-Agent": []string{"grok/1"}},
		TokenInfo:   &canimcp.TokenInfo{Scopes: []string{"s1"}, Expiration: time.Unix(0, 0).UTC(), UserID: "u1", Extra: map[string]any{"k": "v"}},
	}

	out := SharedProfileFromCore(in)
	assert.Equal(t, model.HostGrok, out.HostType)
	assert.Equal(t, model.TransportHTTP, out.Transport)
	assert.Equal(t, model.AuthOAuth, out.AuthMethod)
	assert.True(t, out.Remote)
	assert.True(t, out.Has(model.FeatMCPApps))
	require.NotNil(t, out.ClientInfo)
	assert.Equal(t, model.ClientInfo{Name: "ci", Version: "1", Title: "CI", Description: "ci client"}, *out.ClientInfo)
	assert.Equal(t, "2025-06-18", out.ProtocolVer)
	assert.Equal(t, "grok/1", out.UserAgent)
	assert.Equal(t, http.Header{"User-Agent": []string{"grok/1"}}, out.Headers)
	require.NotNil(t, out.TokenInfo)
	assert.Equal(t, model.TokenInfo{Scopes: []string{"s1"}, Expiration: time.Unix(0, 0).UTC(), UserID: "u1", Extra: map[string]any{"k": "v"}}, *out.TokenInfo)

	// The feature set must be a copy, not a shared map.
	out.Features[model.FeatSinkLocal] = true
	assert.False(t, in.Has(canimcp.FeatSinkLocal))

	// Claim redaction happens at the profile boundary too, so direct callers
	// of SharedProfileFromCore cannot leak raw credentials either.
	sensitive := SharedProfileFromCore(canimcp.Profile{
		TokenInfo: &canimcp.TokenInfo{Extra: map[string]any{"access_token": "raw", "sub": "u"}},
	})
	require.NotNil(t, sensitive.TokenInfo)
	assert.Equal(t, redactedPlaceholder, sensitive.TokenInfo.Extra["access_token"])
	assert.Equal(t, "u", sensitive.TokenInfo.Extra["sub"])

	// Nil wire info stays nil; a zero feature set converts to an empty
	// (non-nil) set, matching the conversion behavior both consumers carry.
	nils := SharedProfileFromCore(canimcp.Profile{})
	assert.Nil(t, nils.ClientInfo)
	assert.Nil(t, nils.TokenInfo)
	assert.Empty(t, nils.Features)
}

func TestToJSONMap(t *testing.T) {
	type payload struct {
		Roots map[string]any `json:"roots"`
	}
	got := ToJSONMap(payload{Roots: map[string]any{"listChanged": true}})
	require.NotNil(t, got)
	assert.Contains(t, got, "roots")

	assert.Nil(t, ToJSONMap(make(chan int)), "unmarshalable values yield nil")
}

// TestRequestCapsDevSnapshotWithSession exercises the initialize-params half
// of the dev snapshot against a real go-sdk session: the snapshot is captured
// from req.Session.InitializeParams(), not from the call-tool _meta.
func TestRequestCapsDevSnapshotWithSession(t *testing.T) {
	captured := make(chan *model.RequestCaps, 1)

	srv := mcp.NewServer(&mcp.Implementation{Name: "caps-test", Version: "0"}, nil)
	srv.AddTool(&mcp.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			build := NewRequestCapsBuilder(RequestCapsOptions{DevSnapshot: true})
			captured <- build(req)
			return &mcp.CallToolResult{}, nil
		})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "caps-test-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	require.NoError(t, err)
	defer session.Close()

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "probe"}); err != nil {
		t.Fatalf("call tool: %v", err)
	}

	rc := <-captured
	require.NotNil(t, rc.Capabilities, "capabilities snapshot captured")
	require.NotNil(t, rc.InitializeParams, "initialize params snapshot captured")
	assert.Equal(t, map[string]any{"name": "caps-test-client", "version": "1"}, rc.InitializeParams["clientInfo"], "snapshot must describe the initialize handshake, not the call meta")
}
