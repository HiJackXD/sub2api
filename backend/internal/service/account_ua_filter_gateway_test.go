//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSelectAccountForModelWithPlatform_UserAgentSkipsMetadataProjection(t *testing.T) {
	// Snapshot shape: only ua_filter_mode and ua_filter_patterns.
	// repository.buildSchedulerMetadataAccount is not imported here; service
	// importing repository would cycle.
	projectedExtra := map[string]any{
		UAFilterExtraModeKey:     "allow",
		UAFilterExtraPatternsKey: []any{"claude-cli/"},
	}
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: projectedExtra},
			{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2.32")
	acc, err := svc.selectAccountForModelWithPlatform(ctx, nil, "", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
}

func TestSelectAccountForModelWithPlatform_UserAgentSkipsRestrictedAccount(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			}},
			{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 1}}
	svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig()}
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2.32")
	acc, err := svc.selectAccountForModelWithPlatform(ctx, nil, "sess", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
	require.Equal(t, int64(0), cache.sessionBindings["sess"])
}

func TestSelectAccountForModelWithPlatform_UserAgentAllBlocked(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{
				"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"},
			}},
		},
		accountsByID: map[int64]*Account{},
	}
	repo.accountsByID[1] = &repo.accounts[0]
	svc := &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}
	_, err := svc.selectAccountForModelWithPlatform(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}

func TestSelectAccountForModelWithPlatform_InternalCallIgnoresFilter(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			}},
		},
		accountsByID: map[int64]*Account{},
	}
	repo.accountsByID[1] = &repo.accounts[0]
	svc := &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}
	acc, err := svc.selectAccountForModelWithPlatform(context.Background(), nil, "", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(1), acc.ID)
}

func TestSelectAccountForModelWithPlatform_UserAgentPassButQuotaBlocked(t *testing.T) {
	groupID := int64(7)
	// RequirePrivacySet does not reject Anthropic: IsPrivacySet is always true
	// for that platform. The model rate limit is the later check, after the
	// loader returns the account and the User-Agent rule accepts it.
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{{
			ID: 1, Platform: PlatformAnthropic, Priority: 1,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
				"model_rate_limits": map[string]any{
					"claude-3-5-sonnet-20241022": map[string]any{
						"rate_limit_reset_at": resetAt,
					},
				},
			},
		}},
		accountsByID: map[int64]*Account{},
	}
	repo.accountsByID[1] = &repo.accounts[0]
	groupRepo := &mockGroupRepoForGateway{
		groups: map[int64]*Group{
			groupID: {ID: groupID, Name: "privacy-required", RequirePrivacySet: true},
		},
	}
	svc := &GatewayService{
		accountRepo: repo,
		groupRepo:   groupRepo,
		cache:       &mockGatewayCacheForPlatform{},
		cfg:         testConfig(),
	}
	_, err := svc.selectAccountForModelWithPlatform(
		WithInboundUserAgent(context.Background(), "claude-cli/2.1.1"),
		&groupID,
		"",
		"claude-3-5-sonnet-20241022",
		nil,
		PlatformAnthropic,
	)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
}

func TestSelectAccountForModelWithPlatform_UserAgentStickyClearDoesNotRebind(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			}},
			{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 1}}
	svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig()}
	ctx := WithStickyBindingClearedByUserAgent(WithInboundUserAgent(context.Background(), "python-requests/2.32"))
	acc, err := svc.selectAccountForModelWithPlatform(ctx, nil, "sess", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(2), acc.ID)
	require.Equal(t, int64(0), cache.sessionBindings["sess"])

	require.NoError(t, svc.BindStickySession(ctx, nil, "sess", acc.ID))
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, nil, "sess", acc.ID))
	require.NoError(t, svc.bindGatewayStickySessionDuringSelection(ctx, nil, "sess", acc.ID))
	require.Equal(t, int64(0), cache.sessionBindings["sess"])
}

func TestSelectAccountForModelWithPlatform_NonStickyUserAgentKeepsSession(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
			}},
			{ID: 2, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true},
			{ID: 3, Platform: PlatformAnthropic, Priority: 3, Status: StatusActive, Schedulable: true},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 2}}
	svc := &GatewayService{accountRepo: repo, cache: cache, cfg: testConfig()}
	excluded := map[int64]struct{}{2: {}}
	acc, err := svc.selectAccountForModelWithPlatform(WithInboundUserAgent(context.Background(), "python-requests/2.32"), nil, "sess", "claude-3-5-sonnet-20241022", excluded, PlatformAnthropic)
	require.NoError(t, err)
	require.Equal(t, int64(3), acc.ID)
	require.Equal(t, int64(3), cache.sessionBindings["sess"])
	require.Zero(t, cache.deletedSessions["sess"])
}

func TestSelectAccountForModelWithPlatform_OtherPlatformIsNotUserAgentFailure(t *testing.T) {
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{{
			ID: 1, Platform: PlatformOpenAI, Priority: 1, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}},
		}},
		accountsByID: map[int64]*Account{},
	}
	repo.accountsByID[1] = &repo.accounts[0]
	svc := &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}
	_, err := svc.selectAccountForModelWithPlatform(WithInboundUserAgent(context.Background(), "python-requests/2"), nil, "", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
}
