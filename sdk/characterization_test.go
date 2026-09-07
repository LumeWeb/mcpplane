package sdk

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.lumeweb.com/mcpplane/model"
)

// These tests pin the behavior of the Tool/ToolResult/AdaptToolHandler
// converters, which are otherwise exercised only indirectly through the CLI's
// hub tests. They guard the converters against unintended changes.

// TestToolConverter pins the descriptor→wire mapping: annotations are always
// populated with all three boolean hints, the input schema is preserved
// verbatim, a valid output schema is emitted, the OpenAI per-tool default
// securitySchemes (oauth2, empty scopes) is injected into _meta, and the
// caller's Meta map is copied — never mutated in place.
func TestToolConverter(t *testing.T) {
	desc := model.ToolDescriptor{
		Name:          "download_file",
		Title:         "Download",
		Description:   "desc",
		ReadOnly:      true,
		Destructive:   false,
		OpenWorldHint: true,
		InputSchema:   []byte(`{"type":"object","properties":{}}`),
		OutputSchema:  []byte(`{"type":"object"}`),
		Meta:          map[string]any{"ui": map[string]any{"resourceUri": "ui://x/y.html"}},
	}
	tool := Tool(desc)
	require.Equal(t, "download_file", tool.Name)
	require.Equal(t, "desc", tool.Description)
	require.JSONEq(t, string(desc.InputSchema), string(tool.InputSchema.(json.RawMessage)))
	require.NotNil(t, tool.Annotations)
	assert.True(t, tool.Annotations.ReadOnlyHint)
	assert.NotNil(t, tool.Annotations.DestructiveHint)
	assert.False(t, *tool.Annotations.DestructiveHint)
	assert.NotNil(t, tool.Annotations.OpenWorldHint)
	assert.True(t, *tool.Annotations.OpenWorldHint)
	require.NotNil(t, tool.OutputSchema)
	require.JSONEq(t, string(desc.OutputSchema), string(tool.OutputSchema.(json.RawMessage)))

	// securitySchemes default injected into a COPY of Meta.
	schemes, ok := tool.Meta["securitySchemes"]
	require.True(t, ok, "securitySchemes = %#v", tool.Meta["securitySchemes"])
	schemesJSON, err := json.Marshal(schemes)
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"oauth2","scopes":[]}]`, string(schemesJSON))
	// The caller's Meta map must not gain securitySchemes (no in-place mutation).
	assert.NotContains(t, desc.Meta, "securitySchemes")

	// Explicit noauth schemes are preserved (not defaulted).
	desc2 := model.ToolDescriptor{Name: "t", Meta: map[string]any{}}
	desc2.SecuritySchemes = []model.SecurityScheme{{Type: "noauth", Scopes: []string{}}}
	gotJSON, err := json.Marshal(Tool(desc2).Meta["securitySchemes"])
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"noauth","scopes":[]}]`, string(gotJSON))

	// An output schema that is not valid JSON is not declared on the wire.
	desc3 := model.ToolDescriptor{Name: "t", OutputSchema: []byte(`not-json`)}
	assert.Nil(t, Tool(desc3).OutputSchema)
}

// TestToolResultConverter pins isError/text and the elicitation path.
func TestToolResultConverter(t *testing.T) {
	res := ToolResult(model.ToolResult{IsError: true, Text: "boom"})
	require.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "boom", text.Text)

	// Elicitation results become input_required with the form schema.
	el := model.FormElicitation("field", "pick one", map[string]any{"type": "object"})
	res = ToolResult(model.ToolResult{Elicitation: &el})
	ir, ok := res.InputRequests["field"]
	require.True(t, ok, "input_request for id")
	ep, ok := ir.(*mcp.ElicitParams)
	require.True(t, ok)
	assert.Equal(t, "form", ep.Mode)
	assert.Equal(t, "pick one", ep.Message)

	// URL-mode elicitation omits the form schema.
	ur := model.ElicitationSpec{ID: "u", URL: "https://example.com/oauth", RequestState: "state42"}
	res = ToolResult(model.ToolResult{Elicitation: &ur})
	ep = res.InputRequests["u"].(*mcp.ElicitParams)
	assert.Equal(t, "url", ep.Mode)
	assert.Equal(t, "https://example.com/oauth", ep.URL)
	assert.Nil(t, ep.RequestedSchema)
	assert.Equal(t, "state42", res.RequestState)
}

// testHandlerRequest builds a CallToolRequest for the adapter tests.
func testHandlerRequest(name string, argsJSON string) *CallToolRequest {
	return &CallToolRequest{Params: &mcp.CallToolParamsRaw{
		Name:      name,
		Arguments: []byte(argsJSON),
	}}
}

// TestAdaptToolHandlerPinsArgDecodeAndErrorHandling exercises the adapter:
// raw JSON args decoded into a map (UseNumber), handler errors become
// IsError text results (not Go errors), and the result passes through.
func TestAdaptToolHandlerPinsArgDecodeAndErrorHandling(t *testing.T) {
	// Happy path: args decoded, integer precision kept via json.Number.
	var seenArgs map[string]any
	handler := AdaptToolHandler(HandlerDeps{}, func(_ context.Context, req model.ToolRequest) (model.ToolResult, error) {
		seenArgs = req.Arguments
		return model.StatusResult(model.StatusOk, "did it", map[string]any{"cid": "Qm1"}), nil
	})
	res, err := handler(context.Background(), testHandlerRequest("upload_url", `{"url":"https://x/y","limit":9007199254740993}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	n, ok := seenArgs["limit"].(json.Number)
	require.True(t, ok, "limit = %#v, want json.Number", seenArgs["limit"])
	assert.Equal(t, "9007199254740993", n.String(), "integer precision preserved")
	require.NotNil(t, res.StructuredContent)

	// Handler error: IsError result carrying the message, nil Go error.
	failing := AdaptToolHandler(HandlerDeps{}, func(_ context.Context, _ model.ToolRequest) (model.ToolResult, error) {
		return model.ToolResult{}, assert.AnError
	})
	res, err = failing(context.Background(), testHandlerRequest("t", `{}`))
	require.NoError(t, err, "adapter converts handler errors to wire errors")
	require.True(t, res.IsError)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, text.Text, "assert.AnError")

	// Malformed JSON arguments: IsError wire result, handler never runs.
	res, err = AdaptToolHandler(HandlerDeps{}, func(context.Context, model.ToolRequest) (model.ToolResult, error) {
		t.Fatal("handler must not run for malformed args")
		return model.ToolResult{}, nil
	})(context.Background(), testHandlerRequest("t", `{bad json`))
	require.NoError(t, err)
	require.True(t, res.IsError)
}

// TestAdaptToolHandlerRequestState pins that the echoed RequestState is
// moved into the arguments under the reserved key (never clobbering an
// argument of the same name).
func TestAdaptToolHandlerRequestState(t *testing.T) {
	var seenArgs map[string]any
	handler := AdaptToolHandler(HandlerDeps{ReservedRequestStateKey: "session_id"},
		func(_ context.Context, req model.ToolRequest) (model.ToolResult, error) {
			seenArgs = req.Arguments
			return model.ToolResult{}, nil
		})
	req := testHandlerRequest("t", `{}`)
	req.Params.RequestState = "sess-1"
	_, err := handler(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", seenArgs["session_id"])

	// An explicit argument beats the echoed state.
	req2 := testHandlerRequest("t", `{"session_id":"explicit"}`)
	req2.Params.RequestState = "sess-2"
	_, err = handler(context.Background(), req2)
	require.NoError(t, err)
	assert.Equal(t, "explicit", seenArgs["session_id"])
}

// TestAcceptedElicitationsMergedIntoArgs pins that accepted form submissions
// on the retry are merged into the arguments keyed by elicitation id.
func TestAcceptedElicitationsMergedIntoArgs(t *testing.T) {
	var seenArgs map[string]any
	handler := AdaptToolHandler(HandlerDeps{}, func(_ context.Context, req model.ToolRequest) (model.ToolResult, error) {
		seenArgs = req.Arguments
		require.True(t, req.InputResponses, "retry carries InputResponses flag")
		return model.ToolResult{}, nil
	})
	req := testHandlerRequest("t", `{"domain":"example.com"}`)
	req.Params.InputResponses = mcp.InputResponseMap{
		"field": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"token": "abc"}},
	}
	_, err := handler(context.Background(), req)
	require.NoError(t, err)
	// The whole accepted form Content map is merged under the elicitation id
	// (the handler unwraps its own fields).
	assert.Equal(t, map[string]any{"token": "abc"}, seenArgs["field"])
	assert.Equal(t, "example.com", seenArgs["domain"])

	// A declined submission is not merged.
	seenArgs = nil
	req2 := testHandlerRequest("t", `{}`)
	req2.Params.InputResponses = mcp.InputResponseMap{
		"field": &mcp.ElicitResult{Action: "decline"},
	}
	_, err = handler(context.Background(), req2)
	require.NoError(t, err)
	assert.NotContains(t, seenArgs, "field")
}

// TestRegisterToolNilHandlers pins the registration seam behavior.
func TestRegisterToolNilHandlers(t *testing.T) {
	srv := NewServer(&ServerOptions{Instructions: "hello"})
	// The go-sdk panics in AddTool when a tool declares no InputSchema, and a
	// nil model.Handler yields a tool whose invocation fails at call time —
	// registration itself succeeds.
	err := RegisterTool(srv, HandlerDeps{}, model.ToolDescriptor{
		Name:        "noop",
		InputSchema: []byte(`{"type":"object","properties":{}}`),
	})
	require.NoError(t, err)

	// Advertised UI capability from every NewServer.
	opts := serverOptions(&ServerOptions{})
	require.NotNil(t, opts.Capabilities)
	_, hasUI := opts.Capabilities.Extensions[UICapabilityID]
	require.True(t, hasUI)
}
