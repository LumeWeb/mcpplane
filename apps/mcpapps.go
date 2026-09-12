// MCP Apps (ext-apps) client-capability handling.
//
// This file re-exports the client's advertised MCP Apps capability reader,
// which lives in the sdk package next to the wire constants it consumes (the
// SDK-neutral capability types live in model; the server-side registration
// bridge (RegisterAppTool / RegisterAppResource) and the SDK wire conversion
// also live in sdk). The re-export keeps in-repo callers compiling.
//
// The protocol constants below mirror @modelcontextprotocol/ext-apps.
package apps

import (
	"go.lumeweb.com/mcpplane/sdk"
)

const (
	// RESOURCE_MIME_TYPE is the MIME type of MCP Apps (mcp-app) resources.
	RESOURCE_MIME_TYPE = "text/html;profile=mcp-app"
	// RESOURCE_URI_META_KEY is the legacy flat _meta key pointing a tool at its
	// UI resource. Kept so older hosts that do not read the nested _meta.ui
	// shape still find the UI.
	RESOURCE_URI_META_KEY = "ui/resourceUri"
	// EXTENSION_ID is the capability extension identifier under which clients
	// advertise MCP Apps support (in client capabilities `extensions`) and
	// servers advertise it back (in server capabilities `extensions`).
	EXTENSION_ID = "io.modelcontextprotocol/ui"
)

// GetClientUICapability reads the typed MCP Apps capability from a client's
// advertised `extensions` (map of extension id -> settings). It returns nil if
// the client did not advertise MCP Apps. Implemented in sdk so the wire seam
// owns the complete go-sdk-adjacent capability surface; the apps package
// re-exports it for compatibility.
var GetClientUICapability = sdk.GetClientUICapability
