package model

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the SDK-neutral model behavior of the status, handoff and
// protocol-model types so that regressions against their established
// semantics are caught explicitly.

func TestStatusResultShape(t *testing.T) {
	r := StatusResult(StatusRunning, "upload started", map[string]any{"handle": "h1"})
	require.False(t, r.IsError)
	sc := r.StructuredContent.(map[string]any)
	assert.Equal(t, StatusRunning, sc["status"])
	assert.Equal(t, "h1", sc["handle"])

	r = StatusResult(StatusOk, "done", nil)
	sc = r.StructuredContent.(map[string]any)
	assert.Equal(t, StatusOk, sc["status"])
}

func TestErrorResultShape(t *testing.T) {
	r := ErrorResult(ErrCodeNotFound, "no such vault", nil)
	require.True(t, r.IsError)
	sc := r.StructuredContent.(map[string]any)
	assert.Equal(t, StatusError, sc["status"])
	assert.Equal(t, ErrCodeNotFound, sc["error"])
	assert.Contains(t, r.Text, "no such vault")
}

func TestRequiresAuthResultShape(t *testing.T) {
	r := RequiresAuthResult("no creds")
	sc := r.StructuredContent.(map[string]any)
	assert.Equal(t, StatusRequiresAuth, sc["status"])
	assert.Equal(t, "auth_sso", sc["resume_tool"])
	assert.Equal(t, "no creds", sc["detail"])
}

func TestNeedsHumanResultShape(t *testing.T) {
	r := NeedsHumanResult(NeedsHuman{
		Reason:     ReasonSSOApproval,
		ActionURL:  "https://example.com/approve",
		Handle:     "abc123",
		ResumeTool: "auth_resume",
		Detail:     "open the link and approve",
	})
	assert.False(t, r.IsError)
	sc, ok := r.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "needs_human", sc["status"])
	assert.Equal(t, ReasonSSOApproval, sc["reason"])
	assert.Equal(t, "https://example.com/approve", sc["action_url"])
	assert.Equal(t, "abc123", sc["handle"])
	assert.Equal(t, "auth_resume", sc["resume_tool"])
	assert.Equal(t, "open the link and approve", sc["detail"])
	assert.Contains(t, r.Text, "needs_human")

	// A hand-off with only a reason omits the optional keys.
	r = NeedsHumanResult(NeedsHuman{Reason: ReasonInteractiveOnly})
	sc = r.StructuredContent.(map[string]any)
	assert.Equal(t, "needs_human", sc["status"])
	_, hasURL := sc["action_url"]
	assert.False(t, hasURL, "action_url omitted when empty")
}

func TestNeedsHumanTextCarriesAllFields(t *testing.T) {
	text := NeedsHumanText(ReasonCredentialEntry, "https://example.com/approve", "hndl789", "vault_create_resume", "open it")
	assert.Contains(t, text, "open https://example.com/approve")
	assert.Contains(t, text, "resume with vault_create_resume")
	assert.Contains(t, text, "handle hndl789")

	// Missing pieces are simply omitted — no dangling punctuation.
	text = NeedsHumanText(ReasonSSOApproval, "https://example.com/login/tok", "", "auth_resume", "")
	assert.Contains(t, text, "open https://example.com/login/tok")
	assert.Contains(t, text, "resume with auth_resume")
	assert.NotContains(t, text, "handle ")

	// detail rides along at the end when present.
	text = NeedsHumanText(ReasonSSOApproval, "", "", "", "just a steer")
	assert.Contains(t, text, " - just a steer")

	// NeedsHumanTextWith adds the revoke hint for in-flight flows.
	text = NeedsHumanTextWith(ReasonSSOApproval, "https://example.com/inuse", "h1", "auth_resume", "auth_sso_revoke", "")
	assert.Contains(t, text, "use auth_sso_revoke to revoke the in-progress flow")
	assert.Contains(t, text, "handle h1")
	assert.Contains(t, text, "resume with auth_resume")
}

func TestDescriptorFromToolPreservesCatalogContract(t *testing.T) {
	entry := &ToolEntry{
		Name:        "pinner_status",
		Title:       "Status",
		Description: "Read status",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"json":{"type":"boolean"}}}`),
		ReadOnly:    true,
		Destructive: false,
	}

	descriptor := DescriptorFromTool(entry)
	require.Equal(t, entry.Name, descriptor.Name)
	require.Equal(t, entry.Title, descriptor.Title)
	require.Equal(t, entry.Description, descriptor.Description)
	require.JSONEq(t, string(entry.InputSchema), string(descriptor.InputSchema))
	require.True(t, descriptor.ReadOnly)
	require.False(t, descriptor.Destructive)
}

func TestToolDescriptorHandlerContract(t *testing.T) {
	handler := ToolHandler(func(_ context.Context, request ToolRequest) (ToolResult, error) {
		return ToolResult{Text: request.Name + ":" + request.Arguments["value"].(string)}, nil
	})

	result, err := handler(context.Background(), ToolRequest{Name: "echo", Arguments: map[string]any{"value": "ok"}})
	require.NoError(t, err)
	require.Equal(t, "echo:ok", result.Text)
}

func TestToolEntryFromDescriptorRoundTrip(t *testing.T) {
	entry := &ToolEntry{
		Name:       "auth_sso",
		Category:   CategoryAccount,
		ReadOnly:   true,
		Handler:    func(context.Context, ToolRequest) (ToolResult, error) { return ToolResult{}, nil },
		MCPTargets: []ToolTarget{{Require: FeatureSet{FeatMCPApps: true}, Visible: true}},
	}
	desc := ToolDescriptorForHandler(entry, entry.Handler)
	require.Equal(t, entry.Name, desc.Name)
	require.NotNil(t, desc.Handler)
	roundTripped := ToolEntryFromDescriptor(desc)
	require.Equal(t, entry.Name, roundTripped.Name)
	require.Equal(t, entry.Category, roundTripped.Category)
	require.Equal(t, InteractionAgentSafe, roundTripped.Interaction)
	require.Equal(t, len(entry.MCPTargets), len(roundTripped.MCPTargets))
	require.NotNil(t, roundTripped.Handler)
}

func TestResourceAndPromptDescriptorContracts(t *testing.T) {
	resource := ResourceDescriptor{
		URI:      "example://account/status",
		MIMEType: "application/json",
		Handler: func(_ context.Context, request ResourceRequest) (ResourceResult, error) {
			return ResourceResult{URI: request.URI, MIMEType: "application/json", Text: "{}"}, nil
		},
	}
	result, err := resource.Handler(context.Background(), ResourceRequest{URI: resource.URI})
	require.NoError(t, err)
	require.Equal(t, resource.URI, result.URI)

	prompt := PromptDescriptor{
		Name:      "setup",
		Arguments: []PromptArgumentDescriptor{{Name: "domain", Required: false}},
		Handler: func(_ context.Context, request PromptRequest) (PromptResult, error) {
			return PromptResult{Messages: []PromptMessage{{Role: "user", Text: request.Arguments["domain"]}}}, nil
		},
	}
	promptResult, err := prompt.Handler(context.Background(), PromptRequest{Arguments: map[string]string{"domain": "example.com"}})
	require.NoError(t, err)
	require.Equal(t, "example.com", promptResult.Messages[0].Text)
}

func TestFormElicitation(t *testing.T) {
	spec := FormElicitation("element", "pick one", map[string]any{"type": "object"})
	require.Equal(t, "element", spec.ID)
	require.Equal(t, "pick one", spec.Message)
	require.Equal(t, map[string]any{"type": "object"}, spec.FormSchema)
	require.Empty(t, spec.URL)
}

func TestClientUICapabilitiesSupportsApps(t *testing.T) {
	// No UI capability => not supported.
	var nilCaps *ClientUICapabilities
	require.False(t, nilCaps.SupportsApps())
	require.False(t, (&ClientUICapabilities{}).SupportsApps())
	require.False(t, (&ClientUICapabilities{MIMETypes: []string{"text/plain"}}).SupportsApps())
	require.True(t, (&ClientUICapabilities{MIMETypes: []string{"text/plain", mcpAppsMIMEType}}).SupportsApps())

	caps := &RequestCaps{UI: &ClientUICapabilities{MIMETypes: []string{mcpAppsMIMEType}}}
	require.True(t, caps.SupportsApps())
	var nilReq *RequestCaps
	require.False(t, nilReq.SupportsApps())
}
