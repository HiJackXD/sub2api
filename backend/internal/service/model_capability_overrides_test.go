package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyCatalogModelCapabilityOverrides_EmptyPolicyKeepsBytes(t *testing.T) {
	t.Parallel()

	body, err := BuildCodexModelsManifest([]string{"custom-model"})
	require.NoError(t, err)

	got, err := applyCatalogModelCapabilityOverrides(body, &Group{Platform: PlatformOpenAI}, nil, PlatformOpenAI, nil, true)
	require.NoError(t, err)
	require.Equal(t, string(body), string(got))
}

func TestApplyCatalogModelCapabilityOverrides_GroupReplacesExistingFieldsOnly(t *testing.T) {
	t.Parallel()

	body, err := BuildCodexModelsManifest([]string{"custom-model"})
	require.NoError(t, err)
	window := int64(128000)
	maxWindow := int64(200000)
	percent := 80
	output := int64(4096)
	group := &Group{Platform: PlatformOpenAI, ModelCapabilityOverrides: ModelCapabilityOverrides{
		ContextWindow:                 &window,
		MaxContextWindow:              &maxWindow,
		EffectiveContextWindowPercent: &percent,
		MaxOutputTokens:               &output,
	}}

	got, err := applyCatalogModelCapabilityOverrides(body, group, nil, PlatformOpenAI, nil, true)
	require.NoError(t, err)
	model := gjson.GetBytes(got, "models.0")
	require.Equal(t, int64(128000), model.Get("context_window").Int())
	require.Equal(t, int64(200000), model.Get("max_context_window").Int())
	require.Equal(t, int64(80), model.Get("effective_context_window_percent").Int())
	require.False(t, model.Get("max_output_tokens").Exists())
	require.Equal(t, codexModelsManifestBodyETag(got), codexModelsManifestBodyETag(got))
}

func TestBuildCodexModelsManifestForAccounts_AccountAgreementAndGroupPriority(t *testing.T) {
	t.Parallel()

	agreed := capabilityAccount("custom-model", 64000)
	peer := capabilityAccount("custom-model", 64000)
	peer.ID = 2
	body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"custom-model"}, []Account{agreed, peer}, nil, nil, true)
	require.NoError(t, err)
	require.Equal(t, int64(64000), gjson.GetBytes(body, "models.0.context_window").Int())

	peer = capabilityAccount("custom-model", 32000)
	peer.ID = 2
	body, err = buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"custom-model"}, []Account{agreed, peer}, nil, nil, true)
	require.NoError(t, err)
	require.Equal(t, int64(configuredCodexFallbackContext), gjson.GetBytes(body, "models.0.context_window").Int())

	groupWindow := int64(32000)
	body, err = buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"custom-model"}, []Account{agreed, peer}, &Group{
		Platform:                 PlatformOpenAI,
		ModelCapabilityOverrides: ModelCapabilityOverrides{ContextWindow: &groupWindow},
	}, nil, true)
	require.NoError(t, err)
	require.Equal(t, int64(32000), gjson.GetBytes(body, "models.0.context_window").Int())
}

func TestMergeGroupConfiguredCodexModels_RecomputesETagAfterOverride(t *testing.T) {
	t.Parallel()

	window := int64(128000)
	group := &Group{
		ID:       9,
		Platform: PlatformOpenAI,
		ModelCapabilityOverrides: ModelCapabilityOverrides{
			ContextWindow: &window,
		},
	}
	body, err := BuildCodexModelsManifest([]string{"custom-model"})
	require.NoError(t, err)
	manifest := &OpenAIModelsResponse{Body: body, ETag: "upstream"}
	svc := &OpenAIGatewayService{accountRepo: capabilityCatalogRepo{accounts: []Account{capabilityAccount("custom-model", 64000)}}}

	require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, ""))
	require.False(t, manifest.NotModified)
	require.Equal(t, int64(128000), gjson.GetBytes(manifest.Body, "models.0.context_window").Int())
	require.Equal(t, codexModelsManifestBodyETag(manifest.Body), manifest.ETag)
	require.NotEqual(t, "upstream", manifest.ETag)

	cached := &OpenAIModelsResponse{Body: append([]byte(nil), manifest.Body...), ETag: manifest.ETag}
	require.NoError(t, svc.MergeGroupConfiguredCodexModels(context.Background(), group, cached, manifest.ETag))
	require.True(t, cached.NotModified)
	require.Empty(t, cached.Body)
}

func TestNormalizeModelCapabilityOverridesExtra(t *testing.T) {
	t.Parallel()

	extra, err := normalizeModelCapabilityOverridesExtra(map[string]any{
		"keep": true,
		domain.ModelCapabilityOverridesExtraKey: map[string]any{
			"context_window": float64(1000),
		},
	})
	require.NoError(t, err)
	require.Equal(t, true, extra["keep"])
	require.Equal(t, float64(1000), extra[domain.ModelCapabilityOverridesExtraKey].(map[string]any)["context_window"])

	_, err = normalizeModelCapabilityOverridesExtra(map[string]any{
		domain.ModelCapabilityOverridesExtraKey: map[string]any{"effective_context_window_percent": float64(101)},
	})
	require.Error(t, err)
}

func capabilityAccount(model string, contextWindow int64) Account {
	return Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{model: model},
		},
		Extra: map[string]any{
			domain.ModelCapabilityOverridesExtraKey: map[string]any{
				"context_window": float64(contextWindow),
			},
		},
	}
}

type capabilityCatalogRepo struct {
	AccountRepository
	accounts []Account
}

func (r capabilityCatalogRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r capabilityCatalogRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}
