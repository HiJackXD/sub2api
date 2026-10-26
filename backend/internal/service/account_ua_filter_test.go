//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

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
