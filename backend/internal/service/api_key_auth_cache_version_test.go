package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyService_RejectsV10AuthSnapshotWithoutModelAllowlist(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-models-list", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  10,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v10 auth snapshot to be rejected after model_allowlist was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV15AuthSnapshotWithoutReasoningEffortPolicy(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-reasoning-mappings", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 15},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v15 auth snapshot to be rejected after reasoning effort policy was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV24AuthSnapshotWithoutModelCapabilityOverrides(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-capability", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 24},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v24 auth snapshot to be rejected after model capability overrides were added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_SnapshotRoundTrip_PreservesModelCapabilityOverrides(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	groupID := int64(9)
	window := int64(128000)
	percent := 90
	apiKey := &APIKey{
		ID:      1,
		UserID:  2,
		GroupID: &groupID,
		Key:     "k-capability",
		Status:  StatusActive,
		User: &User{
			ID:          2,
			Status:      StatusActive,
			Role:        RoleUser,
			Balance:     10,
			Concurrency: 3,
		},
		Group: &Group{
			ID:               groupID,
			Name:             "openai",
			Platform:         PlatformOpenAI,
			Status:           StatusActive,
			SubscriptionType: SubscriptionTypeStandard,
			RateMultiplier:   1,
			ModelCapabilityOverrides: ModelCapabilityOverrides{
				ContextWindow:                 &window,
				EffectiveContextWindowPercent: &percent,
			},
		},
	}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.Equal(t, apiKeyAuthSnapshotVersion, snapshot.Version)
	roundTrip := svc.snapshotToAPIKey(apiKey.Key, snapshot)
	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)
	require.Equal(t, int64(128000), *roundTrip.Group.ModelCapabilityOverrides.ContextWindow)
	require.Equal(t, 90, *roundTrip.Group.ModelCapabilityOverrides.EffectiveContextWindowPercent)
}
