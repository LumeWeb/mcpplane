// Package forgeutil adapts mcpforge's generic, FeatureCarrier-keyed tool
// presentation types onto mcpplane's concrete model.Profile / model.ToolTarget
// seam. mcpforge owns the feature-gated description/target DSL (the roof of
// the dependency), while model owns the neutral tool/target vocabulary; this
// package is the only place the two type families meet, so neither imports
// the other.
package forgeutil

import (
	mcpforge "go.lumeweb.com/mcpforge"

	"go.lumeweb.com/mcpplane/model"
)

// FeaturesFromModel converts a model.FeatureSet to the identical-vocabulary
// mcpforge.FeatureSet. Feature string constants carry the same values in both
// packages ("source-path", "mcp-apps-ui", ...), so the conversion is a direct
// key copy.
func FeaturesFromModel(fs model.FeatureSet) mcpforge.FeatureSet {
	out := make(mcpforge.FeatureSet, len(fs))
	for f, on := range fs {
		out[mcpforge.Feature(f)] = on
	}
	return out
}

// ProfileCarrier adapts a model.Profile to mcpforge.FeatureCarrier so
// mcpforge's builders (DescBuilder, Target resolution, materialization) can be
// driven by a model.Profile.
type ProfileCarrier struct {
	Profile model.Profile
}

// FeatureSet extracts the profile's feature set in mcpforge's vocabulary.
func (c ProfileCarrier) FeatureSet() mcpforge.FeatureSet {
	return FeaturesFromModel(c.Profile.Features)
}

// Targets converts a list of mcpforge targets over ProfileCarrier into the
// neutral model.ToolTarget form carried on model.ToolDescriptor /
// model.ToolEntry. Feature sets are converted key-for-key; a target's
// dynamic DescFunc is wrapped so it is invoked with the same Carrier view at
// resolution time. Self-contained presentation fields (schema, meta, security
// schemes, sensitive flags) are copied verbatim.
func Targets(ts []mcpforge.Target[ProfileCarrier]) []model.ToolTarget {
	if ts == nil {
		return nil
	}
	out := make([]model.ToolTarget, 0, len(ts))
	for _, t := range ts {
		out = append(out, targetFromForge(t))
	}
	return out
}

func targetFromForge(t mcpforge.Target[ProfileCarrier]) model.ToolTarget {
	out := model.ToolTarget{
		Require:         requireFromForge(t.Require),
		Visible:         t.Visible,
		Description:     t.Description,
		InputSchema:     t.InputSchema,
		OutputSchema:    t.OutputSchema,
		Meta:            t.Meta,
		SecuritySchemes: schemesFromForge(t.SecuritySchemes),
		SensitiveFlags:  t.SensitiveFlags,
	}
	if t.DescFunc != nil {
		desc := t.DescFunc
		out.DescFunc = func(p model.Profile) string {
			return desc(ProfileCarrier{Profile: p})
		}
	}
	return out
}

func schemesFromForge(ss []mcpforge.SecurityScheme) []model.SecurityScheme {
	if ss == nil {
		return nil
	}
	out := make([]model.SecurityScheme, 0, len(ss))
	for _, s := range ss {
		out = append(out, model.SecurityScheme{Type: s.Type, Scopes: s.Scopes})
	}
	return out
}

func requireFromForge(fs mcpforge.FeatureSet) model.FeatureSet {
	if fs == nil {
		return nil
	}
	out := make(model.FeatureSet, len(fs))
	for f, on := range fs {
		out[model.Feature(f)] = on
	}
	return out
}

// Fallback builds the single universal target presentation for a tool that
// does not vary by host: a visible target with no feature requirement. It is
// the model-target form of mcpforge.FallbackTarget and supersedes direct use
// of the MCPTargets(Fallback(desc)) call pattern.
func Fallback(desc string) []model.ToolTarget {
	return Targets([]mcpforge.Target[ProfileCarrier]{mcpforge.FallbackTarget[ProfileCarrier](desc)})
}
