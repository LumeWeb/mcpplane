package apps

import (
	"fmt"
	"sync"

	"go.lumeweb.com/mcpplane/model"
	"go.lumeweb.com/mcpplane/sdk"
)

// This file is the light lib layer for authoring ui:// MCP Apps. It collapses
// the three-step manual wiring every app used to repeat — attach _meta.ui to
// the model tool(s), register the HTML resource, register app-only helper
// tools — into one declarative call, so an app is a single AppView value
// instead of a hand-written RegisterXxxApp function that spawns raw
// RegisterAppTool / RegisterAppResource plumbing per app.
//
// It sits on top of the SDK-neutral seam in mcpapps.go (the typed model.AppToolMeta /
// AppResource / RegisterAppTool / RegisterAppResource primitives). Add a new
// app by filling in an AppView and calling AppRegistry.RegisterAppView — no direct
// _meta.ui, resources/list, or CSP manipulation.
//
// All registration state is INSTANCE-scoped on AppRegistry (view-domain
// resolver + tool→view associations). The package keeps no global mutable
// state, so a process can host multiple independent servers (e.g. tests or a
// multi-tenant assembly) without cross-talk.

// AppCatalog is the narrow view of the tool catalog an MCP App needs: look up
// a model-visible tool by name so the app can attach _meta.ui to it. It is
// satisfied by the server's ToolCatalog (which exposes Add/Get/Entries and the
// search/suggest surface) without the apps package importing the assembly —
// the dependency is inverted so apps stays a self-contained framework layer.
type AppCatalog interface {
	// Get returns the registered entry for name, or false if absent.
	Get(name string) (*model.ToolEntry, bool)
}

// AppView declares one ui:// MCP App. A view is an HTML document served at a
// ui:// URI, attached to the existing model-visible tool(s) it renders for,
// plus any app-only helper tools the view calls. AppRegistry.RegisterAppView
// wires all of it at once.
type AppView struct {
	// URI is the ui:// resource URI, e.g. "ui://auth/sso.html".
	URI string
	// Name is a stable, unique resource slug (shown to hosts in resources/list).
	Name string
	// Title is the human-facing view title.
	Title string
	// Description is the resource description surfaced in resources/list. It
	// also serves as the default widget description (_meta.ui.widgetDescription
	// and _meta["openai/widgetDescription"]) that directory submissions expose
	// for the rendered view.
	Description string
	// HTML is the complete, self-contained mcp-app document served at URI.
	// Served verbatim; the sandboxed iframe needs no network request.
	HTML string
	// Domain is the exact HTTPS origin (e.g. "https://mcp.pinner.xyz") the
	// ChatGPT/Apps widget layer attributes this view to, emitted as
	// _meta.ui.domain and _meta["openai/widgetDomain"]. A domain is REQUIRED by
	// the ChatGPT app-directory check and must be a bare origin (no path); all
	// views in one app must share the same origin. When empty, the registry's
	// deployment view-domain resolver supplies the origin (see
	// AppRegistry.SetViewDomainResolver); on a self-hosted server with no
	// resolver and no override no domain is emitted at all — a self-hosted
	// server must never advertise a domain that is not its own.
	Domain string
	// PrefersBorder hints hosts to render the iframe with a border.
	PrefersBorder bool
	// ConnectDomainsFunc, when set, resolves the CSP connectDomains the
	// sandboxed app may reach over the network (e.g. the presigned upload
	// origin an Uppy XHR PUTs to). It is invoked at resource read time — after
	// the transport has resolved its public base/tunnel or loopback origin,
	// which may happen after registration — so the value reflects the live
	// origin rather than a stale registration-time default.
	ConnectDomainsFunc func() []string

	// AttachTo lists existing catalog tool names whose _meta.ui should point at
	// URI. These are the model-visible tools the view renders for. Each must
	// already exist in the catalog; missing names are an error.
	AttachTo []string

	// Helpers are app-only tools the view calls via callServerTool. They are
	// registered with model.ToolVisibilityApp so a UI-capable host exposes them to the
	// iframe while the model never sees them in text-form hosts or the model
	// surface.
	Helpers []model.ToolDescriptor
}

// AppViewInfo is the minimal companion-app description the server emits into a
// model-visible needs_human result so a text-only host can still tell the user
// an interactive MCP App exists alongside the raw URL/handle flow.
type AppViewInfo struct {
	// URI is the ui:// resource URI, e.g. "ui://auth/sso.html".
	URI string
	// Name is the stable resource slug, e.g. "auth-sso".
	Name string
	// Title is the human-facing view title, e.g. "Sign In".
	Title string
}

// AppRegistry holds the per-server app-view registration state: the
// deployment-origin resolver for ui:// views and the tool→view associations
// used to annotate needs_human results with companion-app context. It is safe
// for concurrent use. App registration is additive but may run alongside
// handlers in tests, so the maps are lock-guarded.
type AppRegistry struct {
	mu                 sync.RWMutex
	appViewsByTool     map[string]AppViewInfo
	viewDomainResolver func() string
}

// NewAppRegistry returns an empty app registry. The view-domain resolver is
// unset (zero) — the normal case for a fully self-hosted server with no public
// origin — which means views carry NO domain at all, so a self-hosted server
// never advertises a domain that is not its own.
func NewAppRegistry() *AppRegistry {
	return &AppRegistry{appViewsByTool: map[string]AppViewInfo{}}
}

// SetViewDomainResolver installs the origin the deployment attributes its
// ui:// views to (e.g. the hosted BaseURL origin or the tunnel origin). The
// resolver is invoked at app-registration time so the value reflects the live
// deployment.
func (r *AppRegistry) SetViewDomainResolver(f func() string) {
	r.mu.Lock()
	r.viewDomainResolver = f
	r.mu.Unlock()
}

// ViewDomainResolver returns the currently installed view-domain resolver (or
// nil). Exposed so callers (and tests) can save and restore the deployment
// origin.
func (r *AppRegistry) ViewDomainResolver() func() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.viewDomainResolver
}

// viewDomain returns the origin for a view: an explicit AppView.Domain
// override wins, else the deployment resolver, else empty (no domain emitted).
func (r *AppRegistry) viewDomain(override string) string {
	if override != "" {
		return override
	}
	r.mu.RLock()
	f := r.viewDomainResolver
	r.mu.RUnlock()
	if f == nil {
		return ""
	}
	return f()
}

// AppInfoForTool returns the companion-app info registered for toolName, or the
// zero value if the tool has no attached app.
func (r *AppRegistry) AppInfoForTool(toolName string) (AppViewInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.appViewsByTool[toolName]
	return info, ok
}

// SetAppViewInfo records the companion-app info for a model tool. It is the
// explicit registry write paired with AppInfoForTool, used by the server
// assembly when it needs to associate a tool with a rendered app
// independently of a full RegisterAppView call (and by tests to seed the
// registry).
func (r *AppRegistry) SetAppViewInfo(toolName string, info AppViewInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appViewsByTool[toolName] = info
}

// DeleteAppViewInfo removes any companion-app registration for a model tool.
func (r *AppRegistry) DeleteAppViewInfo(toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.appViewsByTool, toolName)
}

// RegisterAppView wires a complete ui:// MCP App in one call:
//
//   - attaches _meta.ui (resource URI) to every model tool named in AttachTo,
//   - registers the HTML document as a ui:// resource at URI,
//   - registers each Helper as an app-only tool bound to URI.
//
// Returns an error if srv/catalog are nil, URI/Name/Title are empty, any
// AttachTo tool is missing from the catalog, or a helper registers with an
// empty URI. A failed registration never leaves partial wiring behind: every
// attach target is validated and all resource/helper registrations succeed
// before any catalog metadata is attached or any tool→view association is
// recorded. App wiring is additive: existing tools and their plain-host text
// results are preserved, and a tool's existing _meta is extended, never
// replaced.
func (r *AppRegistry) RegisterAppView(srv *sdk.Server, catalog AppCatalog, v AppView) error {
	if srv == nil {
		return fmt.Errorf("mcp: nil official server")
	}
	if catalog == nil {
		return fmt.Errorf("mcp: nil tool catalog")
	}
	if v.URI == "" {
		return fmt.Errorf("mcp: app view requires a uri")
	}
	if v.Name == "" {
		return fmt.Errorf("mcp: app view %q requires a name", v.URI)
	}
	if v.HTML == "" {
		return fmt.Errorf("mcp: app view %q requires html", v.URI)
	}

	// Validate every attach target up front so a missing name fails before
	// any catalog metadata is attached or any tool/helper is registered.
	for _, toolName := range v.AttachTo {
		if _, ok := catalog.Get(toolName); !ok {
			return fmt.Errorf("mcp: app view tool %q not in catalog", toolName)
		}
	}

	domain := r.viewDomain(v.Domain)
	info := AppViewInfo{URI: v.URI, Name: v.Name, Title: v.Title}
	if err := sdk.RegisterAppResource(srv, sdk.AppResource{
		URI:   v.URI,
		Name:  v.Name,
		Title: v.Title,
		// Description is also the widget description for directory surfaces.
		Description: v.Description,
		Meta: model.AppResourceMeta{
			Domain:            domain,
			WidgetDescription: v.Description,
			PrefersBorder:     borderFlag(v.PrefersBorder),
		},
		ConnectDomainsFunc: v.ConnectDomainsFunc,
		HTML:               v.HTML,
	}); err != nil {
		return err
	}

	for _, h := range v.Helpers {
		if err := sdk.RegisterAppTool(srv, h, model.AppToolMeta{
			ResourceURI: v.URI,
			Visibility:  []model.ToolVisibility{model.ToolVisibilityApp},
		}); err != nil {
			return err
		}
	}

	// Only after every registration succeeded, attach _meta.ui and record the
	// tool→view associations, so a failure anywhere above leaves no partial
	// app wiring behind.
	for _, toolName := range v.AttachTo {
		if err := AttachAppMeta(catalog, toolName, v.URI); err != nil {
			return err
		}
		r.mu.Lock()
		r.appViewsByTool[toolName] = info
		r.mu.Unlock()
	}

	return nil
}

// borderFlag returns a heap-allocated bool so PrefersBorder stays an
// omitempty pointer: absent (nil) when false, present true when set.
func borderFlag(v bool) *bool { return new(v) }

// AttachAppMeta attaches the ui:// resource URI to an existing catalog tool's
// _meta.ui (plus the legacy flat key), extending rather than replacing any
// existing metadata. The named tool must already be in the catalog.
func AttachAppMeta(catalog AppCatalog, toolName, resourceURI string) error {
	entry, ok := catalog.Get(toolName)
	if !ok {
		return fmt.Errorf("mcp: app view tool %q not in catalog", toolName)
	}
	meta, err := sdk.MarshalToolMeta(model.AppToolMeta{ResourceURI: resourceURI})
	if err != nil {
		return err
	}
	if entry.Meta == nil {
		entry.Meta = map[string]any{}
	}
	for k, v := range meta {
		entry.Meta[k] = v
	}
	return nil
}
