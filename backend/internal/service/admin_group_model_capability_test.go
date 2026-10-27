//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminService_CreateGroup_RejectsInvalidModelCapabilityOverrides(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}
	window := int64(200000)
	maxWindow := int64(100000)

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:           "g",
		Platform:       PlatformAnthropic,
		RateMultiplier: 1,
		ModelCapabilityOverrides: ModelCapabilityOverrides{
			ContextWindow:    &window,
			MaxContextWindow: &maxWindow,
		},
	})

	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, int32(http.StatusBadRequest), appErr.Code)
	require.Equal(t, "INVALID_MODEL_CAPABILITY_OVERRIDES", appErr.Reason)
	require.Nil(t, repo.created)
}

func TestAdminService_UpdateGroup_StoresModelCapabilityOverrides(t *testing.T) {
	window := int64(128000)
	percent := 80
	repo := &groupRepoStubForAdmin{getByID: &Group{
		ID: 1, Name: "g", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1,
	}}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{
		ModelCapabilityOverrides: &ModelCapabilityOverrides{
			ContextWindow:                 &window,
			EffectiveContextWindowPercent: &percent,
		},
	})

	require.NoError(t, err)
	require.NotNil(t, repo.updated)
	require.Equal(t, int64(128000), *repo.updated.ModelCapabilityOverrides.ContextWindow)
	require.Equal(t, 80, *repo.updated.ModelCapabilityOverrides.EffectiveContextWindowPercent)

	_, err = svc.UpdateGroup(context.Background(), 1, &UpdateGroupInput{
		ModelCapabilityOverrides: &ModelCapabilityOverrides{EffectiveContextWindowPercent: intPtr(101)},
	})
	require.Error(t, err)
	require.Equal(t, "INVALID_MODEL_CAPABILITY_OVERRIDES", infraerrors.FromError(err).Reason)
}
