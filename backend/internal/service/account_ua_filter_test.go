//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountAllowsInboundUserAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		extra   map[string]any
		ua      string
		allowed bool
	}{
		{name: "nil account extra", ua: "", allowed: true},
		{name: "off allows empty", extra: map[string]any{"ua_filter_mode": "off"}, ua: "  ", allowed: true},
		{name: "missing mode allows python", extra: map[string]any{}, ua: "python-requests/2.32", allowed: true},
		{name: "allow hit ignores case and padding", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []any{" Claude-CLI/ "},
		}, ua: "  claude-cli/2.1.1 (external, cli)", allowed: true},
		{name: "allow miss", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"claude-cli/"},
		}, ua: "python-requests/2.32", allowed: false},
		{name: "allow empty ua", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"claude-cli/"},
		}, ua: " \t", allowed: false},
		{name: "block hit", extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []any{"python-requests"},
		}, ua: "Python-Requests/2.32", allowed: false},
		{name: "block miss", extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []string{"python-requests"},
		}, ua: "claude-cli/1.0", allowed: true},
		{name: "block empty ua", extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []string{"python-requests"},
		}, ua: "", allowed: false},
		{name: "dirty mode", extra: map[string]any{"ua_filter_mode": "deny"}, ua: "claude-cli/1", allowed: false},
		{name: "dirty empty patterns", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []any{},
		}, ua: "claude-cli/1", allowed: false},
		{name: "codex desktop preset hits", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"codex desktop"},
		}, ua: "Codex Desktop/1.0", allowed: true},
		{name: "bare codex is not a stored preset", extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"codex desktop"},
		}, ua: "python-codex-script/1", allowed: false},
		{name: "padded allow mode", extra: map[string]any{
			"ua_filter_mode":     " allow",
			"ua_filter_patterns": []string{"claude-cli/"},
		}, ua: "claude-cli/2.1.1", allowed: true},
		{name: "padded off mode", extra: map[string]any{
			"ua_filter_mode":     " off",
			"ua_filter_patterns": []string{"claude-cli/"},
		}, ua: "python-requests/2.32", allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			account := &Account{Extra: tt.extra}
			if tt.name == "nil account extra" {
				account = nil
			}
			require.Equal(t, tt.allowed, account.AllowsInboundUserAgent(tt.ua))
		})
	}
}

func TestInboundUserAgentContext(t *testing.T) {
	t.Parallel()
	_, enforced := InboundUserAgentFromContext(context.Background())
	require.False(t, enforced)
	ctx := WithInboundUserAgent(context.Background(), "")
	ua, enforced := InboundUserAgentFromContext(ctx)
	require.True(t, enforced)
	require.Empty(t, ua)
}

func TestTruncateUserAgentForLog(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", TruncateUserAgentForLog(" \n"))
	require.Equal(t, "a b", TruncateUserAgentForLog("a\nb"))
	long := strings.Repeat("界", 200)
	got := TruncateUserAgentForLog(long)
	require.LessOrEqual(t, len(got), 256)
	require.True(t, strings.HasPrefix(long, got))
}

func TestUAFilterStatsErrorPriority(t *testing.T) {
	t.Parallel()
	var empty uaFilterStats
	require.ErrorIs(t, empty.errorOr(ErrNoAvailableAccounts), ErrNoAvailableAccounts)

	modelMiss := uaFilterStats{}
	require.ErrorIs(t, modelMiss.errorOr(ErrNoAvailableAccounts), ErrNoAvailableAccounts)

	blocked := uaFilterStats{}
	blocked.noteCandidate()
	blocked.noteFail()
	require.ErrorIs(t, blocked.errorOr(ErrNoAvailableAccounts), ErrUserAgentNotAllowed)
	require.Equal(t, UserAgentNotAllowedMessage, ErrUserAgentNotAllowed.Error())

	passed := uaFilterStats{}
	passed.noteCandidate()
	passed.notePass()
	require.ErrorIs(t, passed.errorOr(ErrNoAvailableAccounts), ErrNoAvailableAccounts)
	require.False(t, errors.Is(passed.errorOr(ErrNoAvailableAccounts), ErrUserAgentNotAllowed))
}

func TestNormalizeUserAgentFilterExtra(t *testing.T) {
	t.Parallel()
	normalized, clear, err := NormalizeUserAgentFilterExtra(map[string]any{"other": true})
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, true, normalized["other"])

	normalized, clear, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "off",
		"ua_filter_patterns": []any{"claude-cli/"},
		"other":              "keep",
	})
	require.NoError(t, err)
	require.True(t, clear)
	_, hasMode := normalized["ua_filter_mode"]
	_, hasPatterns := normalized["ua_filter_patterns"]
	require.False(t, hasMode)
	require.False(t, hasPatterns)
	require.Equal(t, "keep", normalized["other"])

	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{"ua_filter_mode": "allow"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ua_filter_patterns")

	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{"ua_filter_mode": "sometimes"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ua_filter_mode")

	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": []any{"a", "curl"},
	})
	require.Error(t, err)

	long := strings.Repeat("x", 129)
	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": []any{long},
	})
	require.Error(t, err)

	duplicates := make([]any, 51)
	for i := range duplicates {
		duplicates[i] = "curl"
	}
	normalized, clear, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": duplicates,
	})
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, []string{"curl"}, normalized["ua_filter_patterns"])

	unique := make([]any, 51)
	for i := range unique {
		unique[i] = strings.Repeat("p", 3) + strings.Repeat("x", i)
	}
	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": unique,
	})
	require.Error(t, err)

	tooLarge := make([]any, 1001)
	for i := range tooLarge {
		tooLarge[i] = "curl"
	}
	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": tooLarge,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
	require.NotContains(t, err.Error(), "at least one pattern")

	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": []any{"cu\nr"},
	})
	require.Error(t, err)

	normalized, clear, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "allow",
		"ua_filter_patterns": []any{" Claude-CLI/ ", "claude-cli/", "  ", "opencode/"},
	})
	require.NoError(t, err)
	require.False(t, clear)
	require.Equal(t, []string{"Claude-CLI/", "opencode/"}, normalized["ua_filter_patterns"])
}

func TestPersistentCandidateAllowsUserAgentOverridesSentinel(t *testing.T) {
	t.Parallel()
	resetAt := time.Now().Add(time.Hour)
	rateLimited := Account{
		ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
		RateLimitResetAt: &resetAt,
	}
	blocked := Account{
		ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}},
	}
	var stats uaFilterStats
	stats.noteCandidate()
	stats.noteFail()
	existing := fmt.Errorf("%w supporting model: claude", ErrNoAvailableAccounts)
	keep := func(account *Account) bool {
		return account != nil && account.Platform == PlatformAnthropic && account.ID != 9
	}
	err := stats.errorOrUnlessPersistentAllows(existing, "python-requests/2.32", []Account{rateLimited, blocked}, keep)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrUserAgentNotAllowed))

	err = stats.errorOrUnlessPersistentAllows(existing, "python-requests/2.32", []Account{blocked}, keep)
	require.ErrorIs(t, err, ErrUserAgentNotAllowed)
}
