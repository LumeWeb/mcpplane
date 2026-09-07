package transfer

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"go.lumeweb.com/mcpplane/model"
	"go.lumeweb.com/mcpplane/toolargs"
)

// sinkSchemaShape is the typed shape of a tool schema embedding a download
// sink, mirroring what the canonical download tool publishes (properties.sink).
type sinkSchemaShape struct {
	Properties struct {
		Sink struct {
			Enum []string `json:"enum"`
		} `json:"sink"`
	} `json:"properties"`
}

// sinkSchema builds the static shape RewriteSinkEnum targets: a top-level
// `sink` property (inlined by the toolargs reflector via DoNotReference).
type sinkSchemaInput struct {
	Sink DownloadSink `json:"sink"`
}

// TestSourceModeEnumValuesContract verifies SourceModeEnumValues stays the
// source of truth for the per-transport enum (derived from the transport's
// generic profile features, resolved via canimcp).
func TestSourceModeEnumValuesContract(t *testing.T) {
	require.Equal(t, []string{"path"}, SourceModeEnumValues(TransportStdio))
	require.Equal(t, []string{"mint"}, SourceModeEnumValues(TransportHTTP))
	require.Equal(t, []string{"url", "data"}, SourceModeEnumValues(TransportOpenAI))
	// Unknown transports advertise nothing rather than guessing.
	require.Nil(t, SourceModeEnumValues(TransportKind("carrier-pigeon")))
}

// TestTransportKindFromFeatures pins the inverse mapping: the mechanism
// source features pin the transport, with stdio > HTTP > OpenAI precedence,
// and co-declared capability features (url/data on an HTTP host's relay tools)
// do NOT flip the transport.
func TestTransportKindFromFeatures(t *testing.T) {
	stdioNum := model.FeatureSet{model.FeatSourcePath: true, model.FeatSinkLocal: true}
	require.Equal(t, TransportStdio, TransportKindFromFeatures(stdioNum))

	httpNum := model.FeatureSet{model.FeatSourceMint: true, model.FeatRemoteAccess: true}
	require.Equal(t, TransportHTTP, TransportKindFromFeatures(httpNum))

	openaiNum := model.FeatureSet{model.FeatSourceURL: true, model.FeatSourceData: true}
	require.Equal(t, TransportOpenAI, TransportKindFromFeatures(openaiNum))

	// Co-declared relay/capability features must NOT flip the transport: an
	// HTTP host's relay tools (upload_url/upload_data) gain url/data features
	// while upload_file stays transport-bound to mint.
	relayPlusHTTP := model.FeatureSet{model.FeatSourceMint: true, model.FeatSourceURL: true, model.FeatSourceData: true}
	require.Equal(t, TransportHTTP, TransportKindFromFeatures(relayPlusHTTP))

	// No mechanism declared: HTTP is the safest degraded default.
	require.Equal(t, TransportHTTP, TransportKindFromFeatures(model.FeatureSet{}))
}

// TestDownloadSinkSchemaEnum regresses the invopop/jsonschema comma-enum
// collapse: a struct enum tag silently publishes only the first value, so
// served sink schemas must be rewritten per transport. On HTTP (dropWired, not
// OpenAI tunnel) both local and drop must be advertised; on the OpenAI tunnel
// and with no filedrop coordinator wired, only local.
func TestDownloadSinkSchemaEnum(t *testing.T) {
	parse := func(t *testing.T, raw json.RawMessage) []string {
		t.Helper()
		var s sinkSchemaShape
		require.NoError(t, json.Unmarshal(raw, &s))
		return s.Properties.Sink.Enum
	}

	onHTTP := RewriteSinkEnum(toolargs.ToolSchemaFor[sinkSchemaInput](), true, false)
	require.Equal(t, []string{"local", "drop"}, parse(t, onHTTP),
		"HTTP transport with a filedrop coordinator must advertise local+drop")

	onTunnel := RewriteSinkEnum(toolargs.ToolSchemaFor[sinkSchemaInput](), true, true)
	require.Equal(t, []string{"local"}, parse(t, onTunnel),
		"OpenAI tunnel (no reachable mux) must not advertise drop")

	noDrop := RewriteSinkEnum(toolargs.ToolSchemaFor[sinkSchemaInput](), false, false)
	require.Equal(t, []string{"local"}, parse(t, noDrop),
		"no filedrop coordinator wired -> local only")
}

// TestDownloadSinkEnumMalformedSchemaPassthrough pins the degrade-to-static
// contract: a schema without the expected properties.sink shape (or invalid
// JSON) is returned unchanged, never a panic.
func TestDownloadSinkEnumMalformedSchemaPassthrough(t *testing.T) {
	original := json.RawMessage(`{"type":"object"}`)
	require.Equal(t, json.RawMessage(original), RewriteSinkEnum(original, true, false))
	broken := json.RawMessage(`{not json`)
	require.Equal(t, json.RawMessage(broken), RewriteSinkEnum(broken, true, false))
}

// TestSizeLimitedWriter pins the loud-failure writer cap: writes that would
// cross the byte cap fail with the distinguished over-cap error (mappable to a
// 413 by the filedrop GET handler), bytes within the cap pass through, and
// maxBytes <= 0 means unlimited.
func TestSizeLimitedWriter(t *testing.T) {
	// Within cap: all bytes flow through.
	buf := &bytes.Buffer{}
	w := NewSizeLimitedWriter(buf, 16)
	n, err := w.Write(bytes.Repeat([]byte("a"), 10))
	require.NoError(t, err)
	require.Equal(t, 10, n)
	// Crossing the cap fails loudly; nothing partial is emitted.
	_, err = w.Write(bytes.Repeat([]byte("b"), 10))
	require.Error(t, err)
	require.True(t, IsDownloadTooLarge(err))
	require.Equal(t, 10, buf.Len(), "over-cap write must not emit partial bytes")

	// The error reports the configured cap.
	w2 := NewSizeLimitedWriter(io.Discard, 4)
	_, err = w2.Write(bytes.Repeat([]byte("c"), 5))
	require.Error(t, err)
	require.Contains(t, err.Error(), "4")

	// maxBytes <= 0: unlimited.
	buf3 := &bytes.Buffer{}
	w3 := NewSizeLimitedWriter(buf3, 0)
	_, err = w3.Write(bytes.Repeat([]byte("d"), 64))
	require.NoError(t, err)
	require.Equal(t, 64, buf3.Len())

	// IsDownloadTooLarge only matches the over-cap error, not others.
	require.False(t, IsDownloadTooLarge(io.EOF))
}
