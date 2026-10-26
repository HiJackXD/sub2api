//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIUserAgent_PreviousResponseMissKeepsMapping(t *testing.T) {
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2.32")
	groupID := int64(25)
	account := Account{
		ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"codex_cli_rs/"},
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_ua", account.ID, time.Hour))

	selection, err := svc.selectAccountByPreviousResponseIDForCapability(ctx, &groupID, "resp_ua", "gpt-5.1", nil, "", false)
	require.NoError(t, err)
	require.Nil(t, selection)
	boundID, getErr := store.GetResponseAccount(ctx, groupID, "resp_ua")
	require.NoError(t, getErr)
	require.Equal(t, account.ID, boundID)
}

func TestOpenAIUserAgent_SchedulerSkipsRestrictedSticky(t *testing.T) {
	groupID := int64(71)
	restricted := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}},
	}
	open := restricted
	open.ID = 2
	open.Priority = 2
	open.Extra = nil
	accounts := []Account{restricted, open}
	pointers := []*Account{&accounts[0], &accounts[1]}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess": 1}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: pointers, accountsByID: map[int64]*Account{1: pointers[0], 2: pointers[1]}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2.32")
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.GreaterOrEqual(t, cache.deletedSessions["openai:sess"], 1)
}

func TestOpenAIUserAgent_SchedulerAllBlocked(t *testing.T) {
	groupID := int64(72)
	only := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}},
	}
	account := only
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	_, _, err := svc.SelectAccountWithScheduler(WithInboundUserAgent(context.Background(), "python-requests/2.32"), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}

func TestOpenAIUserAgent_InternalCallKeepsRestrictedSticky(t *testing.T) {
	groupID := int64(73)
	restricted := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}},
	}
	account := restricted
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess": 1}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "sess", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)
	require.Equal(t, 0, cache.deletedSessions["openai:sess"])
}

func TestOpenAIUserAgent_StickyClearDoesNotRebind(t *testing.T) {
	groupID := int64(74)
	restricted := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}},
	}
	open := restricted
	open.ID = 2
	open.Priority = 2
	open.Extra = nil
	accounts := []Account{restricted, open}
	pointers := []*Account{&accounts[0], &accounts[1]}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess": 1}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: pointers, accountsByID: map[int64]*Account{1: pointers[0], 2: pointers[1]}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := WithStickyBindingClearedByUserAgent(WithInboundUserAgent(context.Background(), "python-requests/2.32"))
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.GreaterOrEqual(t, cache.deletedSessions["openai:sess"], 1)
	require.Zero(t, cache.sessionBindings["openai:sess"])

	require.NoError(t, svc.BindStickySession(ctx, &groupID, "sess", selection.Account.ID))
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &groupID, "sess", selection.Account.ID))
	require.NoError(t, svc.bindOpenAIStickySessionDuringSelection(ctx, &groupID, "sess", selection.Account.ID))
	require.Zero(t, cache.sessionBindings["openai:sess"])
}

func TestOpenAIUserAgent_ModelMissIsNotUserAgentFailure(t *testing.T) {
	groupID := int64(75)
	only := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-4o": "gpt-4o"}},
		Extra:       map[string]any{"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"}},
	}
	account := only
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess": 1}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	_, _, err := svc.SelectAccountWithScheduler(WithInboundUserAgent(context.Background(), "python-requests/2.32"), &groupID, "", "sess", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
	require.Contains(t, err.Error(), "model_not_supported")
	require.Equal(t, 0, cache.deletedSessions["openai:sess"])
	require.Equal(t, int64(1), cache.sessionBindings["openai:sess"])
}

func TestOpenAIUserAgent_LegacyLoadBatchStickyClearDoesNotRebind(t *testing.T) {
	groupID := int64(76)
	restricted := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}},
	}
	open := restricted
	open.ID = 2
	open.Priority = 2
	open.Extra = nil
	accounts := []Account{restricted, open}
	pointers := []*Account{&accounts[0], &accounts[1]}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:sess": 1}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("false"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: pointers, accountsByID: map[int64]*Account{1: pointers[0], 2: pointers[1]}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := WithStickyBindingClearedByUserAgent(WithInboundUserAgent(context.Background(), "python-requests/2.32"))
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "sess", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.GreaterOrEqual(t, cache.deletedSessions["openai:sess"], 1)
	require.Zero(t, cache.sessionBindings["openai:sess"])
	require.NoError(t, svc.BindStickySession(ctx, &groupID, "sess", selection.Account.ID))
	require.Zero(t, cache.sessionBindings["openai:sess"])
}

func TestOpenAIUserAgent_AllowedAccountTemporarilyUnavailable(t *testing.T) {
	groupID := int64(77)
	// 5h quota auto-pause is the smallest existing fixture that keeps an OpenAI
	// account schedulable and model-compatible while the initial filter skips it.
	allowed := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{
			"ua_filter_mode":          "allow",
			"ua_filter_patterns":      []string{"python-requests"},
			"codex_5h_used_percent":   95.0,
			"auto_pause_5h_threshold": 0.95,
		},
	}
	blocked := allowed
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Extra = map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": []string{"python-requests"},
	}
	accounts := []Account{allowed, blocked}
	pointers := []*Account{&accounts[0], &accounts[1]}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: pointers, accountsByID: map[int64]*Account{1: pointers[0], 2: pointers[1]}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	ctx := WithInboundUserAgent(context.Background(), "python-requests/2.32")
	_, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))
}

func TestOpenAIUserAgent_AllTemporarilyUnavailableAndBlocked(t *testing.T) {
	groupID := int64(78)
	only := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
		Extra: map[string]any{
			"ua_filter_mode":          "block",
			"ua_filter_patterns":      []string{"python-requests"},
			"codex_5h_used_percent":   95.0,
			"auto_pause_5h_threshold": 0.95,
		},
	}
	account := only
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                &config.Config{RunMode: config.RunModeStandard},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		schedulerSnapshot:  &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account}}},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	_, _, err := svc.SelectAccountWithScheduler(WithInboundUserAgent(context.Background(), "python-requests/2.32"), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
	require.False(t, errors.Is(err, ErrNoAvailableAccounts))
}

func TestOpenAIUserAgent_StatsPointer(t *testing.T) {
	account := &Account{ID: 9, Extra: map[string]any{
		"ua_filter_mode": "block", "ua_filter_patterns": []string{"python-requests"},
	}}
	ctx, stats := withUAFilterStats(WithInboundUserAgent(context.Background(), "python-requests/2.32"))
	require.True(t, userAgentBlocksCandidate(ctx, account, nil))
	var direct uaFilterStats
	require.True(t, userAgentBlocksCandidate(ctx, account, &direct))
	require.Equal(t, 1, stats.candidates)
	require.Equal(t, 1, stats.failed)
	require.Equal(t, 0, stats.passed)
	require.Equal(t, 1, direct.candidates)
	require.Equal(t, 1, direct.failed)
	require.Equal(t, 0, direct.passed)
}
