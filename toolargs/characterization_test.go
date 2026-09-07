package toolargs

import (
	"testing"

	"encoding/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.lumeweb.com/mcpplane/model"
)

// These tests document the current behavior of DecodeToolArgs, DecodeArgsFor,
// WrapResult and ToolSchemaFor so that any change to the decode/schema/wrap
// helpers that alters their behavior is caught explicitly.

type uploadInput struct {
	URL   string `json:"url" jsonschema:"description=source URL"`
	Wait  bool   `json:"wait"`
	Limit int64  `json:"limit,omitempty"`
}

func TestDecodeToolArgsDecodesMap(t *testing.T) {
	req := model.ToolRequest{Arguments: map[string]any{
		"url":   "https://example.com/f.bin",
		"wait":  true,
		"limit": float64(2048),
	}}
	in, err := DecodeToolArgs[uploadInput](req)
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/f.bin", in.URL)
	assert.True(t, in.Wait)
	assert.EqualValues(t, 2048, in.Limit)
}

func TestDecodeToolArgsEmptyArgumentsKeepZeroValues(t *testing.T) {
	in, err := DecodeToolArgs[uploadInput](model.ToolRequest{})
	require.NoError(t, err)
	assert.Equal(t, uploadInput{}, in, "absent fields keep Go zero values")
}

func TestDecodeToolArgsRejectsTypeMismatch(t *testing.T) {
	req := model.ToolRequest{Arguments: map[string]any{"limit": "not-a-number"}}
	_, err := DecodeToolArgs[uploadInput](req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode tool arguments")
}

func TestDecodeArgsForUnconfiguredFailsFast(t *testing.T) {
	_, err := DecodeArgsFor[uploadInput]("upload_url", false, model.ToolRequest{Arguments: map[string]any{"url": "x"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload_url handler is not configured")

	in, err := DecodeArgsFor[uploadInput]("upload_url", true, model.ToolRequest{Arguments: map[string]any{"url": "https://x/y"}})
	require.NoError(t, err)
	assert.Equal(t, "https://x/y", in.URL)
}

func TestWrapResultPropagatesErrorAndEnvelopesSuccess(t *testing.T) {
	sendErr := assert.AnError
	res, err := WrapResult(nil, sendErr, "uploading…")
	assert.Nil(t, res.StructuredContent)
	assert.ErrorIs(t, err, sendErr)

	type result struct {
		CID string `json:"cid"`
	}
	res, err = WrapResult(result{CID: "Qm1"}, nil, "uploaded…")
	require.NoError(t, err)
	assert.Equal(t, `{"status":"ok","cid":"Qm1"}`, res.Text)
	structured, ok := res.StructuredContent.(result)
	require.True(t, ok)
	assert.Equal(t, "Qm1", structured.CID)
}

func TestToolSchemaForReflectsTags(t *testing.T) {
	raw := ToolSchemaFor[uploadInput]()
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))
	assert.Equal(t, "object", schema["type"])
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	urlProp, ok := props["url"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, urlProp["description"], "source URL")
}
