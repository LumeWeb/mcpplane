package sdk

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"go.lumeweb.com/mcpplane/model"
)

// resourceTemplateCapture registers a template on a fresh server and returns
// the ResourceRequest the handler received after an in-memory client read it,
// so template-variable resolution is exercised through the public protocol
// surface.
func resourceTemplateCapture(t *testing.T, descriptor model.ResourceTemplateDescriptor, requestURI string) model.ResourceRequest {
	t.Helper()
	var got model.ResourceRequest
	srv := NewServer(nil)
	descriptor.Handler = func(ctx context.Context, req model.ResourceRequest) (model.ResourceResult, error) {
		got = req
		return model.ResourceResult{URI: req.URI, Text: "ok"}, nil
	}
	require.NoError(t, RegisterResources(srv, nil, []model.ResourceTemplateDescriptor{descriptor}))

	cs := sdkTestClient(t, srv)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: requestURI})
	require.NoError(t, err)
	require.NotEmpty(t, res.Contents)
	return got
}

// TestResourceTemplateArgumentsPerVariable pins that resource-template
// variables are resolved per variable, not by regexp capture-group index:
// uritemplate's Regexp() yields one capture group per EXPRESSION, so the
// index-mapping approach misassigns multi-variable expressions like {cid,tag}
// (the whole comma-joined value lands on the first variable and the second is
// lost). The handler must receive each variable's own value.
func TestResourceTemplateArgumentsPerVariable(t *testing.T) {
	got := resourceTemplateCapture(t, model.ResourceTemplateDescriptor{
		URITemplate: "vault://{owner}/{cid,tag}",
		Name:        "vault-entry",
	}, "vault://alice/bafy123,doc")

	require.Equal(t, "vault://alice/bafy123,doc", got.URI)
	require.Equal(t, map[string]string{
		"owner": "alice",
		"cid":   "bafy123",
		"tag":   "doc",
	}, got.Arguments)
}

// TestResourceTemplateArgumentsRepeatedVariable pins the repeated-variable
// case: when one variable appears in two expressions, Varnames() dedupes so
// the index-based mapping aligns the wrong groups and keeps only the first
// occurrence; per-variable Match resolves both occurrences.
func TestResourceTemplateArgumentsRepeatedVariable(t *testing.T) {
	got := resourceTemplateCapture(t, model.ResourceTemplateDescriptor{
		URITemplate: "thing{a}/sub{a}",
		Name:        "thing",
	}, "thingx/suby")

	require.Equal(t, "thingx/suby", got.URI)
	require.Equal(t, map[string]string{"a": "x,y"}, got.Arguments)
}

// TestResourceTemplateArgumentsMissingVariablesSkipped pins that variables not
// present in the read URI are absent from Arguments rather than empty strings.
func TestResourceTemplateArgumentsMissingVariablesSkipped(t *testing.T) {
	got := resourceTemplateCapture(t, model.ResourceTemplateDescriptor{
		URITemplate: "vault://{owner}{/extra}",
		Name:        "vault-entry",
	}, "vault://alice")

	require.Equal(t, map[string]string{"owner": "alice"}, got.Arguments)
}
