package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFeatureSetHasHasAllClone pins the FeatureSet semantics ported from
// hostenv: Has on absent keys is false, HasAll is all-or-nothing, and Clone
// produces an independent copy (mutation of the clone must not leak into the
// original map).
func TestFeatureSetHasHasAllClone(t *testing.T) {
	fs := FeatureSet{FeatMCPApps: true, FeatElicitation: false} // explicit false
	require.True(t, fs.Has(FeatMCPApps))
	require.False(t, fs.Has(FeatElicitation))
	require.False(t, FeatureSet{}.Has(FeatMCPApps), "missing key is false")

	require.True(t, fs.HasAll(FeatureSet{FeatMCPApps: true}))
	require.False(t, fs.HasAll(FeatureSet{FeatMCPApps: true, FeatSourcePath: true}))
	// Note the hostenv semantics: a requirement entry explicitly set to false
	// is NOT satisfied — HasAll requires the lookup (fs[f]) to be true.
	require.False(t, fs.HasAll(FeatureSet{FeatElicitation: false}))
	require.True(t, FeatureSet{}.HasAll(nil), "empty requirement is trivially satisfied")

	clone := fs.Clone()
	require.Len(t, clone, len(fs))
	clone[FeatSourcePath] = true
	require.False(t, fs.Has(FeatSourcePath), "clone mutation must not leak into the original")
}

// TestProfileChecks pins the Profile convenience predicates ported verbatim
// from hostenv.PlatformProfile.
func TestProfileChecks(t *testing.T) {
	p := Profile{
		HostType:  HostClaude,
		Transport: TransportStdio,
		Features:  FeatureSet{FeatSourcePath: true, FeatSinkLocal: true},
	}
	require.True(t, p.Has(FeatSourcePath))
	require.False(t, p.Has(FeatRemoteAccess))
	require.True(t, p.IsTransport(TransportStdio))
	require.False(t, p.IsTransport(TransportHTTP))
	require.True(t, p.IsHost(HostClaude))
	require.False(t, p.IsHost(HostUnknown))
	require.False(t, p.IsHost(HostOpenAI))

	// CloneFeatures: overlaying features must not mutate the shared static map.
	overlay := p.CloneFeatures()
	overlay.Features[FeatFileHostInput] = true
	require.True(t, overlay.Has(FeatFileHostInput))
	require.False(t, p.Has(FeatFileHostInput), "static profile's feature map must stay untouched")
}

// TestToolTargetEligibility pins the ToolTarget shape: Require is a neutral
// FeatureSet and DescFunc receives the neutral Profile.
func TestToolTargetEligibility(t *testing.T) {
	target := ToolTarget{
		Require:  FeatureSet{FeatSourceMint: true},
		Visible:  true,
		DescFunc: func(p Profile) string { return "transport " + string(p.Transport) },
	}
	satisfies := func(p Profile) bool { return p.Features.HasAll(target.Require) }
	httpProfile := Profile{Transport: TransportHTTP, Features: FeatureSet{FeatSourceMint: true, FeatSinkLocal: true}}
	stdioProfile := Profile{Transport: TransportStdio, Features: FeatureSet{FeatSourcePath: true, FeatSinkLocal: true}}
	require.True(t, satisfies(httpProfile))
	require.False(t, satisfies(stdioProfile), "target requires FeatSourceMint")
	require.False(t, satisfies(Profile{Transport: TransportHTTP}), "nil feature set never satisfies")
}
