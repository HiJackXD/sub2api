//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{{
			ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Priority: 1,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{
				"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"},
				"quota_limit": 1.0, "quota_used": 2.0,
			},
		}},
		accountsByID: map[int64]*Account{},
	}
	repo.accountsByID[1] = &repo.accounts[0]
	svc := &GatewayService{accountRepo: repo, cache: &mockGatewayCacheForPlatform{}, cfg: testConfig()}
	_, err := svc.selectAccountForModelWithPlatform(WithInboundUserAgent(context.Background(), "claude-cli/2.1.1"), nil, "", "claude-3-5-sonnet-20241022", nil, PlatformAnthropic)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
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
