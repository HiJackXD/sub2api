//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func newUAGeminiService(accounts []Account, bindings map[string]int64) (*GeminiMessagesCompatService, *mockGatewayCacheForGemini) {
	repo := &mockAccountRepoForGemini{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForGemini{sessionBindings: bindings}
	return &GeminiMessagesCompatService{accountRepo: repo, groupRepo: &mockGroupRepoForGemini{groups: map[int64]*Group{}}, cache: cache}, cache
}

func TestGeminiUserAgent_SkipsRestrictedSticky(t *testing.T) {
	svc, cache := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
		{ID: 2, Platform: PlatformGemini, Priority: 2, Status: StatusActive, Schedulable: true},
	}, map[string]int64{"gemini:sess": 1})
	acc, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "sess", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
	require.Equal(t, 1, cache.deletedSessions["gemini:sess"])
}

func TestGeminiUserAgent_AllBlocked(t *testing.T) {
	svc, _ := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}}},
	}, nil)
	_, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "", "gemini-2.5-flash", nil)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}

func TestGeminiUserAgent_InternalCallKeepsRestrictedAccount(t *testing.T) {
	svc, _ := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
	}, nil)
	acc, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), acc.ID)
}

func TestGeminiUserAgent_ModelsEndpointSkipsEmptyUA(t *testing.T) {
	svc, _ := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
		{ID: 2, Platform: PlatformGemini, Priority: 2, Status: StatusActive, Schedulable: true},
	}, nil)
	acc, err := svc.SelectAccountForModelsEndpoints(WithInboundUserAgent(context.Background(), ""), nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
}

func TestGeminiUserAgent_StickyClearDoesNotRebind(t *testing.T) {
	svc, cache := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
		{ID: 2, Platform: PlatformGemini, Priority: 2, Status: StatusActive, Schedulable: true},
	}, map[string]int64{"gemini:sess": 1})
	ctx := WithStickyBindingClearedByUserAgent(WithInboundUserAgent(context.Background(), "python-requests/2"))
	acc, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "sess", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
	require.Equal(t, 1, cache.deletedSessions["gemini:sess"])
	_, stillBound := cache.sessionBindings["gemini:sess"]
	require.False(t, stillBound)
	require.True(t, stickyBindingClearedByUserAgentRequest(ctx))
}

func TestGeminiUserAgent_AntigravityStickyUsesSameFilter(t *testing.T) {
	svc, cache := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformAntigravity, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"mixed_scheduling": true, "ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
		{ID: 2, Platform: PlatformAntigravity, Priority: 2, Status: StatusActive, Schedulable: true, Extra: map[string]any{"mixed_scheduling": true}},
	}, map[string]int64{"gemini:sess": 1})
	acc, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "sess", "gemini-2.5-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
	require.Equal(t, 1, cache.deletedSessions["gemini:sess"])
	_, stillBound := cache.sessionBindings["gemini:sess"]
	require.False(t, stillBound)
}

func TestGeminiUserAgent_AntigravityForcePlatformAllBlocked(t *testing.T) {
	svc, _ := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformAntigravity, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}}},
	}, nil)
	ctx := context.WithValue(WithInboundUserAgent(context.Background(), "python-requests/2"), ctxkey.ForcePlatform, PlatformAntigravity)
	_, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gemini-2.5-flash", nil)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}

func TestGeminiUserAgent_AntigravityModelMissIsNotUserAgentFailure(t *testing.T) {
	svc, cache := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformAntigravity, Priority: 1, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}, Extra: map[string]any{"mixed_scheduling": true, "ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
	}, map[string]int64{"gemini:sess": 1})
	_, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "sess", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
	require.Contains(t, err.Error(), "supporting model")
	require.Equal(t, int64(1), cache.sessionBindings["gemini:sess"])
	require.Zero(t, cache.deletedSessions["gemini:sess"])
}

func TestGeminiUserAgent_AntigravityPlatformMissIsNotUserAgentFailure(t *testing.T) {
	svc, cache := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformAntigravity, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}}},
	}, map[string]int64{"gemini:sess": 1})
	_, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "sess", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
	require.Contains(t, err.Error(), "supporting model")
	require.Equal(t, int64(1), cache.sessionBindings["gemini:sess"])
	require.Zero(t, cache.deletedSessions["gemini:sess"])
}

func TestGeminiUserAgent_PassThenRateLimitKeepsGeminiError(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	svc, _ := newUAGeminiService([]Account{{
		ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			"model_rate_limits": map[string]any{
				"gemini-2.5-flash": map[string]any{"rate_limit_reset_at": resetAt},
			},
		},
	}}, nil)
	_, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "claude-cli/2.1.1"), nil, "", "gemini-2.5-flash", nil)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
	require.Contains(t, err.Error(), "no available")
}

func TestGeminiUserAgent_RateLimitedAccountStillCountsAsUserAgentFailure(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	svc, _ := newUAGeminiService([]Account{{
		ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			"model_rate_limits": map[string]any{
				"gemini-2.5-flash": map[string]any{"rate_limit_reset_at": resetAt},
			},
		},
	}}, nil)
	_, err := svc.SelectAccountForModelWithExclusions(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "", "gemini-2.5-flash", nil)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}

func TestGeminiUserAgent_QuotaAfterUserAgent(t *testing.T) {
	svc, _ := newUAGeminiService(nil, nil)
	passThenQuota := Account{
		ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			"ua_filter_mode": "allow", "ua_filter_patterns": []string{"python-requests"},
			"quota_limit": 1.0, "quota_used": 2.0,
		},
	}
	blocked := Account{
		ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Priority: 2, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{
			"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"},
			"quota_limit": 1.0, "quota_used": 2.0,
		},
	}
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2")
	selected, err := svc.selectBestGeminiAccount(ctx, []Account{passThenQuota, blocked}, "gemini-2.5-flash", nil, PlatformGemini, true)
	require.NoError(t, err)
	require.Nil(t, selected)

	selected, err = svc.selectBestGeminiAccount(ctx, []Account{blocked}, "gemini-2.5-flash", nil, PlatformGemini, true)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
	require.Nil(t, selected)
}

func TestGeminiUserAgent_ModelsEndpointAllBlocked(t *testing.T) {
	svc, _ := newUAGeminiService([]Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}}},
	}, nil)
	_, err := svc.SelectAccountForModelsEndpoints(WithInboundUserAgent(context.Background(), ""), nil)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}
