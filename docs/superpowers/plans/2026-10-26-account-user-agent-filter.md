# Account User-Agent Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an upstream account optionally allow or block inbound User-Agents, skip that account when the client does not match, and return 403 only when no candidate account accepts the client.

**Architecture:** Store `ua_filter_mode` and `ua_filter_patterns` in `accounts.extra`. A pure matcher decides one account. API-key middleware records the raw inbound User-Agent on the request context. Each user-request scheduler counts model-and-platform candidates, applies the matcher before transient schedulability, and returns `ErrUserAgentNotAllowed` when every candidate fails. Handlers map that sentinel to HTTP 403 before the existing no-account classifier. The admin UI writes literal substrings, including harness presets.

**Tech Stack:** Go 1.27 service layer, Gin handlers, PostgreSQL JSONB merge, Vue 3 admin modals, Vitest.

## Global Constraints

- Rules live on the upstream account, not on a group or an API key.
- `ua_filter_mode` is `off`, `allow`, or `block`. Missing and `off` both mean disabled.
- `ua_filter_patterns` exists only for `allow` and `block`.
- Saving `off` deletes both keys. Other `extra` keys stay untouched.
- `allow` and `block` are mutually exclusive.
- An invalid mode returns 400.
- `allow` or `block` requires at least one pattern after normalization, otherwise 400.
- Normalization trims each pattern, drops empty strings, dedupes case-insensitively, and keeps the first original casing.
- Each pattern is 3–128 Unicode code points, at most 50 patterns, and ASCII control characters U+0000–U+001F and U+007F are rejected.
- Admin API errors use the existing admin error envelope and name `ua_filter_mode` or `ua_filter_patterns`.
- Stored dirty data fails closed at request time.
- Presets expand to literals. Claude Code writes `claude-cli/`. Codex writes `codex_cli_rs/`, `codex-tui/`, `codex_vscode/`, `codex_vscode_copilot/`, `codex_app/`, `codex_chatgpt_desktop/`, `codex_atlas/`, `codex_exec/`, `codex_sdk_ts/`, and `codex desktop`. OpenCode writes `opencode/`. Do not store `codex ` or bare `codex`.
- Disabled mode allows every User-Agent, including an empty one.
- Enabled mode rejects a missing, empty, or whitespace-only User-Agent.
- Matching trims and lowercases both sides, uses substring contains, and succeeds on any one pattern.
- `allow` passes only on a hit. `block` fails on a hit and passes when nothing hits.
- Internal calls with no inbound User-Agent context do not apply the filter.
- User-request gateways set the context even when the header is absent, storing an empty string and marking the request.
- A rejected account is excluded for that request only: no status change, no failure count, no concurrency slot, and no upstream request.
- Apply the filter in every `GatewayService` user-request selection loop, including sticky hits; in the OpenAI scheduler candidate filter, session sticky, and `previous_response_id` pin; and in `GeminiMessagesCompatService` selection and sticky hits, including Antigravity accounts selected there.
- A sticky account blocked by the rule is not used, and that session binding is deleted.
- A `previous_response_id` account blocked by the rule is not used. The stored response mapping is not deleted.
- Candidate set means accounts already accepted for this selection by group, platform, and model, before this rule and before transient unschedulability. `excludedIDs` are not candidates.
- If any candidate passes the rule and the existing scheduler, select it.
- If at least one candidate passes the rule but all of those are transiently unavailable, return the existing no-available-accounts error.
- If no candidate passes the rule, return `ErrUserAgentNotAllowed`.
- If the candidate set is empty before this rule, return the existing corresponding error, not `ErrUserAgentNotAllowed`.
- `ErrUserAgentNotAllowed` works with `errors.Is`. Map it to HTTP 403 before the generic no-account handler, before any upstream connection.
- Error type is `forbidden_error`. Message is exactly `No available account accepts this User-Agent`.
- OpenAI envelope is `{"error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}`.
- Anthropic envelope is `{"type":"error","error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}`.
- The response contains neither mode, patterns, nor account ID. WebSocket requests fail with the same 403 before upgrade or upstream dial.
- Each skipped account logs Info with account ID, mode, and the User-Agent truncated on a code-point boundary to at most 256 bytes. Empty stays empty. Newlines become spaces. The log stays off the client response.
- Do not replace `claude_code_only` or `codex_cli_only`, and do not read the outbound or header-overridden User-Agent.
- No regex, prefix mode, exact-match mode, or stored preset IDs.

---

### Task 1: Pure matcher, context, and sentinel

**Files:**
- Create: `backend/internal/service/account_ua_filter.go`
- Create: `backend/internal/service/account_ua_filter_test.go`
- Modify: `backend/internal/pkg/ctxkey/ctxkey.go`

**Interfaces:**
- Consumes: `Account.Extra map[string]any`
- Produces:
  - `const UAFilterExtraModeKey = "ua_filter_mode"`
  - `const UAFilterExtraPatternsKey = "ua_filter_patterns"`
  - `const UAFilterModeOff = "off"`
  - `const UAFilterModeAllow = "allow"`
  - `const UAFilterModeBlock = "block"`
  - `const UserAgentNotAllowedMessage = "No available account accepts this User-Agent"`
  - `var ErrUserAgentNotAllowed = errors.New(UserAgentNotAllowedMessage)`
  - `func (a *Account) AllowsInboundUserAgent(userAgent string) bool`
  - `func (a *Account) UserAgentFilterMode() string` returns `off` when disabled or missing; returns the raw mode string when it is `allow` or `block`; returns `invalid` for any other non-empty mode
  - `func WithInboundUserAgent(ctx context.Context, userAgent string) context.Context`
  - `func InboundUserAgentFromContext(ctx context.Context) (userAgent string, enforced bool)`
  - `func TruncateUserAgentForLog(userAgent string) string`
  - `func LogUserAgentFiltered(accountID int64, mode, userAgent string)`
  - `type uaFilterStats struct` with `noteCandidate()`, `notePass()`, `noteFail()`, and `func (s uaFilterStats) errorOr(existing error) error`

- [ ] **Step 1: Write the failing matcher tests**

Create `backend/internal/service/account_ua_filter_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestAccountAllowsInboundUserAgent|TestInboundUserAgentContext|TestTruncateUserAgentForLog|TestUAFilterStatsErrorPriority' -count=1`

Expected: FAIL because `AllowsInboundUserAgent` is undefined.

- [ ] **Step 3: Add the context key**

In `backend/internal/pkg/ctxkey/ctxkey.go`, add this constant next to `ClaudeCodeVersion`:

```go
// InboundUserAgent 标记用户请求的原始 User-Agent。键不存在表示内部调用。
InboundUserAgent Key = "ctx_inbound_user_agent"
```

- [ ] **Step 4: Implement the matcher**

Create `backend/internal/service/account_ua_filter.go`:

```go
package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	UAFilterExtraModeKey     = "ua_filter_mode"
	UAFilterExtraPatternsKey = "ua_filter_patterns"
	UAFilterModeOff          = "off"
	UAFilterModeAllow        = "allow"
	UAFilterModeBlock        = "block"
	UserAgentNotAllowedMessage = "No available account accepts this User-Agent"
	uaFilterInvalidMode      = "invalid"
	uaFilterLogMaxBytes      = 256
)

var ErrUserAgentNotAllowed = errors.New(UserAgentNotAllowedMessage)

type inboundUserAgentValue struct {
	Value string
}

func WithInboundUserAgent(ctx context.Context, userAgent string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxkey.InboundUserAgent, inboundUserAgentValue{Value: userAgent})
}

func InboundUserAgentFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(ctxkey.InboundUserAgent).(inboundUserAgentValue)
	if !ok {
		return "", false
	}
	return value.Value, true
}

func (a *Account) UserAgentFilterMode() string {
	if a == nil || a.Extra == nil {
		return UAFilterModeOff
	}
	raw, ok := a.Extra[UAFilterExtraModeKey].(string)
	if !ok || strings.TrimSpace(raw) == "" || raw == UAFilterModeOff {
		return UAFilterModeOff
	}
	if raw == UAFilterModeAllow || raw == UAFilterModeBlock {
		return raw
	}
	return uaFilterInvalidMode
}

func (a *Account) AllowsInboundUserAgent(userAgent string) bool {
	mode := a.UserAgentFilterMode()
	if mode == UAFilterModeOff {
		return true
	}
	if strings.TrimSpace(userAgent) == "" {
		return false
	}
	patterns, ok := uaFilterStoredPatterns(a.Extra)
	if !ok {
		return false
	}
	haystack := strings.ToLower(strings.TrimSpace(userAgent))
	hit := false
	for _, pattern := range patterns {
		if strings.Contains(haystack, strings.ToLower(strings.TrimSpace(pattern))) {
			hit = true
			break
		}
	}
	if mode == UAFilterModeAllow {
		return hit
	}
	return !hit
}

func uaFilterStoredPatterns(extra map[string]any) ([]string, bool) {
	if extra == nil {
		return nil, false
	}
	raw, exists := extra[UAFilterExtraPatternsKey]
	if !exists || raw == nil {
		return nil, false
	}
	var items []string
	switch typed := raw.(type) {
	case []string:
		items = typed
	case []any:
		items = make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			items = append(items, text)
		}
	default:
		return nil, false
	}
	valid := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if !uaFilterPatternAcceptable(item) {
			return nil, false
		}
		valid = append(valid, item)
	}
	if len(valid) == 0 {
		return nil, false
	}
	return valid, true
}

func uaFilterPatternAcceptable(pattern string) bool {
	count := utf8.RuneCountInString(pattern)
	if count < 3 || count > 128 {
		return false
	}
	for _, r := range pattern {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}

func TruncateUserAgentForLog(userAgent string) string {
	userAgent = strings.NewReplacer("\r", " ", "\n", " ").Replace(userAgent)
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) <= uaFilterLogMaxBytes {
		return userAgent
	}
	end := 0
	for end < len(userAgent) {
		_, size := utf8.DecodeRuneInString(userAgent[end:])
		if end+size > uaFilterLogMaxBytes {
			break
		}
		end += size
	}
	return userAgent[:end]
}

func LogUserAgentFiltered(accountID int64, mode, userAgent string) {
	slog.Info("account_user_agent_filtered",
		"account_id", accountID,
		"ua_filter_mode", mode,
		"user_agent", TruncateUserAgentForLog(userAgent),
	)
}

type uaFilterStats struct {
	candidates int
	passed     int
	failed     int
}

func (s *uaFilterStats) noteCandidate() {
	if s != nil {
		s.candidates++
	}
}

func (s *uaFilterStats) notePass() {
	if s != nil {
		s.passed++
	}
}

func (s *uaFilterStats) noteFail() {
	if s != nil {
		s.failed++
	}
}

func (s uaFilterStats) errorOr(existing error) error {
	if s.candidates > 0 && s.passed == 0 && s.failed == s.candidates {
		return ErrUserAgentNotAllowed
	}
	return existing
}

func userAgentBlocksCandidate(ctx context.Context, account *Account, stats *uaFilterStats) bool {
	userAgent, enforced := InboundUserAgentFromContext(ctx)
	if !enforced || account == nil {
		return false
	}
	stats.noteCandidate()
	if account.AllowsInboundUserAgent(userAgent) {
		stats.notePass()
		return false
	}
	stats.noteFail()
	LogUserAgentFiltered(account.ID, account.UserAgentFilterMode(), userAgent)
	return true
}
```

`ErrUserAgentNotAllowed` is declared only in this file. Do not add a second declaration beside `ErrNoAvailableAccounts`.

- [ ] **Step 5: Re-run the tests**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestAccountAllowsInboundUserAgent|TestInboundUserAgentContext|TestTruncateUserAgentForLog|TestUAFilterStatsErrorPriority' -count=1`

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/service/account_ua_filter.go backend/internal/service/account_ua_filter_test.go backend/internal/pkg/ctxkey/ctxkey.go
git commit -m "feat: match per-account inbound user-agent rules"
```

---

### Task 2: Save validation and JSONB deletion

**Files:**
- Modify: `backend/internal/service/account_ua_filter.go`
- Modify: `backend/internal/service/account_ua_filter_test.go`
- Modify: `backend/internal/service/admin_account.go` (`CreateAccount` after line 495, `UpdateAccount` after line 603, duplicate path after line 321, `UpdateAccountExtra` after line 929, `BulkUpdateAccounts` after line 967)
- Modify: `backend/internal/service/account_service.go` (`Create` before the repo create, `Update` when `req.Extra != nil`)
- Modify: `backend/internal/service/account_service.go` `AccountBulkUpdate` struct
- Modify: `backend/internal/repository/account_repo.go` `BulkUpdate` extra expression around line 3131 and `UpdateExtra` around line 2763

**Interfaces:**
- Consumes: Task 1 constants and `uaFilterPatternAcceptable`
- Produces: `func NormalizeUserAgentFilterExtra(extra map[string]any) (normalized map[string]any, clear bool, err error)`
  - `clear == true` means both keys must be removed from stored JSON, not merely set to `off`
  - `err` is `infraerrors.BadRequest` with code `UA_FILTER_MODE_INVALID` or `UA_FILTER_PATTERNS_INVALID`

- [ ] **Step 1: Write failing normalization tests**

Append to `backend/internal/service/account_ua_filter_test.go`:

```go
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

	tooMany := make([]any, 51)
	for i := range tooMany {
		tooMany[i] = "curl"
	}
	_, _, err = NormalizeUserAgentFilterExtra(map[string]any{
		"ua_filter_mode":     "block",
		"ua_filter_patterns": tooMany,
	})
	require.Error(t, err)

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
```

- [ ] **Step 2: Run the test and confirm it fails**

Run: `cd backend && go test -tags unit ./internal/service/ -run TestNormalizeUserAgentFilterExtra -count=1`

Expected: FAIL because `NormalizeUserAgentFilterExtra` is undefined.

- [ ] **Step 3: Implement normalization**

Append to `backend/internal/service/account_ua_filter.go`:

```go
func NormalizeUserAgentFilterExtra(extra map[string]any) (map[string]any, bool, error) {
	if extra == nil {
		return nil, false, nil
	}
	delete(extra, UAFilterClearOnMergeKey)
	_, hasMode := extra[UAFilterExtraModeKey]
	_, hasPatterns := extra[UAFilterExtraPatternsKey]
	if !hasMode && !hasPatterns {
		return extra, false, nil
	}
	rawMode, modeIsString := extra[UAFilterExtraModeKey].(string)
	if hasMode && !modeIsString {
		return nil, false, infraerrors.BadRequest("UA_FILTER_MODE_INVALID", "ua_filter_mode must be off, allow, or block")
	}
	modeRaw := strings.TrimSpace(rawMode)
	if !hasMode || modeRaw == "" || modeRaw == UAFilterModeOff {
		if hasMode && modeRaw != "" && modeRaw != UAFilterModeOff {
			return nil, false, infraerrors.BadRequest("UA_FILTER_MODE_INVALID", "ua_filter_mode must be off, allow, or block")
		}
		delete(extra, UAFilterExtraModeKey)
		delete(extra, UAFilterExtraPatternsKey)
		return extra, true, nil
	}
	if modeRaw != UAFilterModeAllow && modeRaw != UAFilterModeBlock {
		return nil, false, infraerrors.BadRequest("UA_FILTER_MODE_INVALID", "ua_filter_mode must be off, allow, or block")
	}
	patterns, err := normalizeUAFilterPatternInput(extra[UAFilterExtraPatternsKey])
	if err != nil {
		return nil, false, err
	}
	extra[UAFilterExtraModeKey] = modeRaw
	extra[UAFilterExtraPatternsKey] = patterns
	return extra, false, nil
}

func normalizeUAFilterPatternInput(raw any) ([]string, error) {
	var items []any
	switch typed := raw.(type) {
	case []string:
		items = make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	case []any:
		items = typed
	default:
		return nil, infraerrors.BadRequest("UA_FILTER_PATTERNS_INVALID", "ua_filter_patterns must contain at least one pattern when ua_filter_mode is allow or block")
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, infraerrors.BadRequest("UA_FILTER_PATTERNS_INVALID", "ua_filter_patterns contains an invalid pattern")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if !uaFilterPatternAcceptable(text) {
			return nil, infraerrors.BadRequest("UA_FILTER_PATTERNS_INVALID", "ua_filter_patterns contains an invalid pattern")
		}
		key := strings.ToLower(text)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
	}
	if len(out) == 0 || len(out) > 50 {
		return nil, infraerrors.BadRequest("UA_FILTER_PATTERNS_INVALID", "ua_filter_patterns must contain at least one pattern when ua_filter_mode is allow or block")
	}
	return out, nil
}
```

Add this import to `account_ua_filter.go`:

```go
infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
```

Add this constant next to the other UA filter constants:

```go
UAFilterClearOnMergeKey = "__ua_filter_clear__"
```

- [ ] **Step 4: Call the normalizer on every write path**

In `CreateAccount`, after `ValidateUpstreamRequestIDHeaderExtra`:

```go
accountExtra, _, err = NormalizeUserAgentFilterExtra(accountExtra)
if err != nil {
	return nil, err
}
```

Apply the same two lines to the duplicate-account extra (`accountExtra` after line 321) and to `normalizedExtra` in `UpdateAccount` after the upstream-request-id validation.

In `AccountService.Create`, before building the account:

```go
extra, _, err := NormalizeUserAgentFilterExtra(req.Extra)
if err != nil {
	return nil, err
}
req.Extra = extra
```

In `AccountService.Update`, inside `if req.Extra != nil`, after the local `extra` map is copied and before `prepareCodexFingerprintExtraForUpdate`:

```go
normalized, _, err := NormalizeUserAgentFilterExtra(extra)
if err != nil {
	return nil, err
}
extra = normalized
```

In `UpdateAccountExtra` and at the start of `BulkUpdateAccounts`, after the existing managed-key deletes:

```go
normalized, clearUAFilter, err := NormalizeUserAgentFilterExtra(updates)
if err != nil {
	return err
}
updates = normalized
```

For bulk, `updates` is `input.Extra`. Keep `clearUAFilter` on the input by adding this field to `AccountBulkUpdate`:

```go
ClearUserAgentFilter bool
```

Set `repoUpdates.ClearUserAgentFilter = clearUAFilter` where `repoUpdates` is built. Do not change the `UpdateExtra` method signature. When `UpdateAccountExtra` gets `clearUAFilter == true`, set the internal sentinel before the repository call:

```go
if clearUAFilter {
	if updates == nil {
		updates = map[string]any{}
	}
	updates[UAFilterClearOnMergeKey] = true
}
```

- [ ] **Step 5: Delete keys in JSONB merges**

In `accountRepository.BulkUpdate`, add `updates.ClearUserAgentFilter` to the condition that builds `extraExpression`. After the existing `||` payload is appended:

```go
if updates.ClearUserAgentFilter {
	extraExpression = "(" + extraExpression + ") - 'ua_filter_mode' - 'ua_filter_patterns'"
}
```

Do not put `ua_filter_mode` or `ua_filter_patterns` back into `updates.Extra` when `ClearUserAgentFilter` is true. `NormalizeUserAgentFilterExtra` already deleted them.

In `accountRepository.UpdateExtra`, before `json.Marshal`, remove the sentinel and remember it:

```go
clearUserAgentFilter := false
if flag, ok := updates[service.UAFilterClearOnMergeKey].(bool); ok && flag {
	clearUserAgentFilter = true
	delete(updates, service.UAFilterClearOnMergeKey)
}
```

Skip the empty-payload return when `clearUserAgentFilter` is true. When the payload is empty, use `extraExpression := "COALESCE(extra, '{}'::jsonb)"` and still execute the update. When the flag is true, append:

```go
extraExpression = "(" + extraExpression + ") - 'ua_filter_mode' - 'ua_filter_patterns'"
```

`NormalizeUserAgentFilterExtra` deletes `UAFilterClearOnMergeKey` from client input, so only the service can set it after validation.

- [ ] **Step 6: Re-run normalization tests and the package compile**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestNormalizeUserAgentFilterExtra|TestAccountAllowsInboundUserAgent' -count=1`

Expected: PASS

Run: `cd backend && go test -tags unit ./internal/repository/ -count=1 -timeout 120s`

Expected: existing repository tests PASS. `UpdateExtra` keeps its current signature.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/service/account_ua_filter.go backend/internal/service/account_ua_filter_test.go backend/internal/service/admin_account.go backend/internal/service/account_service.go backend/internal/repository/account_repo.go
git commit -m "feat: validate account user-agent filter settings"
```

---

### Task 3: Mark user requests with the inbound User-Agent

**Files:**
- Modify: `backend/internal/server/middleware/api_key_auth.go` immediately after line 167
- Create: `backend/internal/server/middleware/api_key_auth_ua_filter_test.go`

**Interfaces:**
- Consumes: `service.WithInboundUserAgent`
- Produces: every request that passes API-key authentication carries the context value, including an empty header

- [ ] **Step 1: Write the failing middleware test**

Create `backend/internal/server/middleware/api_key_auth_ua_filter_test.go`. The test calls `setInboundUserAgent` directly. The implementation of that function is in Step 3.

```go
//go:build unit

package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetInboundUserAgentMarksMissingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	setInboundUserAgent(c)
	ua, enforced := service.InboundUserAgentFromContext(c.Request.Context())
	require.True(t, enforced)
	require.Empty(t, ua)

	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.1")
	setInboundUserAgent(c)
	ua, enforced = service.InboundUserAgentFromContext(c.Request.Context())
	require.True(t, enforced)
	require.Equal(t, "claude-cli/2.1.1", ua)
}
```

- [ ] **Step 2: Run it and confirm failure**

Run: `cd backend && go test -tags unit ./internal/server/middleware/ -run TestSetInboundUserAgentMarksMissingHeader -count=1`

Expected: FAIL because `setInboundUserAgent` is undefined.

- [ ] **Step 3: Implement and call it from auth**

Put `setInboundUserAgent` in `api_key_auth.go`. Call it immediately after the existing line:

```go
ctx := context.WithValue(c.Request.Context(), ctxkey.UserID, apiKey.User.ID)
c.Request = c.Request.WithContext(ctx)
setInboundUserAgent(c)
```

`setInboundUserAgent` must read the context after that assignment so `UserID` is preserved:

```go
func setInboundUserAgent(c *gin.Context) {
	c.Request = c.Request.WithContext(service.WithInboundUserAgent(c.Request.Context(), c.GetHeader("User-Agent")))
}
```

- [ ] **Step 4: Re-run the test**

Run: `cd backend && go test -tags unit ./internal/server/middleware/ -run TestSetInboundUserAgentMarksMissingHeader -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/server/middleware/api_key_auth.go backend/internal/server/middleware/api_key_auth_ua_filter_test.go
git commit -m "feat: record inbound user-agent on api key requests"
```

---

### Task 4: GatewayService selection

**Files:**
- Modify: `backend/internal/service/gateway_scheduling.go`
- Create: `backend/internal/service/account_ua_filter_gateway_test.go`

**Interfaces:**
- Consumes: `userAgentBlocksCandidate`, `uaFilterStats.errorOr`, `cache.DeleteSessionAccountID`
- Produces: `SelectAccountWithLoadAwareness`, `selectAccountForModelWithPlatform`, and `selectAccountWithMixedScheduling` obey the four selection outcomes

For an ordinary candidate, after platform and model checks have succeeded and before quota, RPM, window cost, concurrency, or schedulable checks:

```go
if userAgentBlocksCandidate(ctx, account, &uaStats) {
	continue
}
```

Do not delete a session binding here. A blocked non-sticky candidate must not clear the session.

For a sticky hit, after the same platform and model checks and before slot acquisition:

```go
if userAgentBlocksCandidate(ctx, account, &uaStats) {
	if sessionHash != "" && s.cache != nil && account.ID == stickyAccountID {
		_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
	}
	// Do not select this account. Fall through to the normal candidate loop.
} else if the existing sticky checks pass {
	// existing sticky selection
}
```

Delete the binding only when the blocked account is the current sticky account.

Before every existing `return nil, ErrNoAvailableAccounts` or wrapped model-unavailable error that is reached after candidate evaluation, return `uaStats.errorOr(existingErr)` only when `uaStats.candidates > 0`. When the loaded account list is empty before model filtering, keep the existing error unchanged. A channel-pricing or composite-route failure that happens before accounts are loaded also stays unchanged.

- [ ] **Step 1: Write the failing gateway test**

Create `backend/internal/service/account_ua_filter_gateway_test.go` with `//go:build unit`. Reuse `mockAccountRepoForPlatform`, `mockGatewayCacheForPlatform`, and `testConfig` from `gateway_multiplatform_test.go`.

```go
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
	require.Equal(t, 0, cache.sessionBindings["sess"])
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
```

- [ ] **Step 2: Run the tests and confirm failure**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestSelectAccountForModelWithPlatform_UserAgent' -count=1`

Expected: FAIL because the restricted account is still selected or the sentinel is not returned.

- [ ] **Step 3: Wire the three GatewayService selectors**

In `selectAccountForModelWithPlatform`, `selectAccountWithMixedScheduling`, and `SelectAccountWithLoadAwareness`:

- Declare `var uaStats uaFilterStats` at the start of the function.
- In each sticky block, after `platformOK` and `modelSupported` are true and before slot acquisition, call `userAgentBlocksCandidate`. On true, delete that session binding and do not return the sticky account.
- In each candidate loop, after the platform and model `continue` checks and before schedulable, quota, RPM, and window-cost checks, call `userAgentBlocksCandidate` and `continue` on true. Do not delete the session binding for a non-sticky candidate.
- At the final empty-candidate return, use `return nil, uaStats.errorOr(ErrNoAvailableAccounts)`.
- Leave early returns that happen before any account is inspected on their existing errors.

Do not change `checkClaudeCodeRestriction` or `codex_cli_only`.

- [ ] **Step 4: Re-run the gateway tests and the existing platform test**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestSelectAccountForModelWithPlatform_UserAgent|TestGatewayService_SelectAccountForModelWithPlatform' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/gateway_scheduling.go backend/internal/service/account_ua_filter_gateway_test.go
git commit -m "feat: skip gateway accounts rejected by user-agent"
```

---

### Task 5: OpenAI scheduler, sticky session, and previous response

**Files:**
- Modify: `backend/internal/service/openai_account_scheduler.go` (`selectBySessionHash` around line 531, `isAccountRequestCompatibleReason` around line 1815, `selectByLoadBalance` around line 1410)
- Modify: `backend/internal/service/openai_gateway_scheduling.go` (`selectAccountWithLoadAwareness` and its sticky check around line 981 and candidate loop around line 1214)
- Modify: `backend/internal/service/openai_ws_forwarder_support.go` `selectAccountByPreviousResponseIDForCapability` before line 480
- Create: `backend/internal/service/account_ua_filter_openai_test.go`

**Interfaces:**
- Consumes: `userAgentBlocksCandidate`, `ErrUserAgentNotAllowed`
- Produces: OpenAI selection returns the sentinel without deleting a `previous_response_id` mapping; session sticky is deleted when that pinned account fails

- [ ] **Step 1: Write the failing OpenAI tests**

Create `backend/internal/service/account_ua_filter_openai_test.go`:

```go
//go:build unit

package service

import (
	"context"
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
```

`schedulerTestConcurrencyCache{}` is a valid zero value: its methods are defined on the struct and do not call the embedded interface. The session cache key is `openai:sess` because `getStickySessionAccountID` prefixes the raw hash `sess`.

- [ ] **Step 2: Run the tests and confirm failure**

Run: `cd backend && go test -tags unit ./internal/service/ -run TestOpenAIUserAgent -count=1`

Expected: FAIL

- [ ] **Step 3: Implement the OpenAI checks**

In `selectBySessionHash`, after the account is loaded and before `shouldClearStickySession`:

```go
var uaStats uaFilterStats
if userAgentBlocksCandidate(ctx, account, &uaStats) {
	clearBinding()
	return nil, false, nil
}
```

In `selectAccountByPreviousResponseIDForCapability`, after the account is resolved and before `tryAcquireAccountSlot`:

```go
var uaStats uaFilterStats
if userAgentBlocksCandidate(ctx, account, &uaStats) {
	return nil, nil
}
```

Do not call `DeleteSessionAccountID` or `BindResponseAccount` on that branch.

In `isAccountRequestCompatibleReason`, after `supportsOpenAIRequestModel` succeeds and before runtime quota or rate-limit reasons, record the User-Agent failure as reason `"user_agent"` by calling `userAgentBlocksCandidate`. Only call it when the model check passed. A model miss must return `"model_not_supported"` without touching `uaFilterStats`.

Thread one `uaFilterStats` through `selectByLoadBalance` via the request context:

```go
type uaFilterStatsContextKey struct{}

func withUAFilterStats(ctx context.Context) (context.Context, *uaFilterStats) {
	stats := &uaFilterStats{}
	return context.WithValue(ctx, uaFilterStatsContextKey{}, stats), stats
}
```

Change `userAgentBlocksCandidate` to use the stats pointer from context when the argument is nil, and to use the argument when it is non-nil. Update the Task 1 function so both forms write the same counters. At the end of `selectByLoadBalance`, when the current code would return `noAvailableOpenAISelectionError`, return `stats.errorOr(thatError)` instead. `selectAccountWithScheduler` already returns non-`ErrNoAvailableAccounts` errors without the quarantine retry; do not add `ErrUserAgentNotAllowed` to that retry condition.

Apply the same stats and sticky deletion in `OpenAIGatewayService.selectAccountWithLoadAwareness`, which is the legacy load-aware path.

- [ ] **Step 4: Re-run the OpenAI tests**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestOpenAIUserAgent|TestDefaultOpenAIAccountScheduler' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/openai_account_scheduler.go backend/internal/service/openai_gateway_scheduling.go backend/internal/service/openai_ws_forwarder_support.go backend/internal/service/account_ua_filter_openai_test.go backend/internal/service/account_ua_filter.go
git commit -m "feat: apply user-agent filter to openai account selection"
```

---

### Task 6: Gemini and Antigravity selection

**Files:**
- Modify: `backend/internal/service/gemini_messages_compat_service.go` (`tryStickySessionHit` around line 231, `selectBestGeminiAccount` around line 345)
- Modify: `backend/internal/service/gemini_models_endpoints.go` (`SelectAccountForModelsEndpoints` around line 49)
- Create: `backend/internal/service/account_ua_filter_gemini_test.go`

**Interfaces:**
- Consumes: `userAgentBlocksCandidate`, `ErrUserAgentNotAllowed`
- Produces: Gemini and Antigravity selections made by `GeminiMessagesCompatService` follow the same four outcomes

- [ ] **Step 1: Write the failing Gemini test**

Create `backend/internal/service/account_ua_filter_gemini_test.go`:

```go
//go:build unit

package service

import (
	"context"
	"testing"

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
```

- [ ] **Step 2: Run the test and confirm failure**

Run: `cd backend && go test -tags unit ./internal/service/ -run TestGeminiUserAgent -count=1`

Expected: FAIL

- [ ] **Step 3: Filter inside Gemini selection**

In `tryStickySessionHit`, after `shouldClearStickySession` and after platform and model usability are known, call `userAgentBlocksCandidate`. On true, delete `cacheKey` through `DeleteSessionAccountID` and return nil.

In `selectBestGeminiAccount`, split the current `isAccountUsableForRequestWithPrecheck` check so model and platform eligibility are known first. Count only those accounts. A User-Agent failure `continue`s before rate-limit and quota checks. Change the function to return `(*Account, error)`. When the selected account is nil, return `uaStats.errorOr(nil)`. Callers that currently turn nil into `no available Gemini accounts` must return `ErrUserAgentNotAllowed` when `errorOr` does, and keep the old Gemini error when the stats error is nil.

`SelectAccountForModelsEndpoints` must run the same candidate check. An empty enforced User-Agent fails an enabled account. Antigravity accounts selected by this service use the same function, so no second implementation is required.

- [ ] **Step 4: Re-run the Gemini tests**

Run: `cd backend && go test -tags unit ./internal/service/ -run 'TestGeminiUserAgent|TestGeminiMessages' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/service/gemini_messages_compat_service.go backend/internal/service/gemini_models_endpoints.go backend/internal/service/account_ua_filter_gemini_test.go
git commit -m "feat: apply user-agent filter to gemini account selection"
```

---

### Task 7: HTTP 403 mapping

**Files:**
- Modify: `backend/internal/handler/no_account_error.go`
- Modify: `backend/internal/handler/no_account_error_test.go`
- Modify every handler branch that calls `classifyNoAccountErrorFromGin` or `classifyOpenAICompatibleNoAccountErrorFromGin`:
  - `backend/internal/handler/gateway_handler.go` near lines 357, 691, and 2235
  - `backend/internal/handler/gateway_handler_chat_completions.go` near line 200
  - `backend/internal/handler/gateway_handler_responses.go` near line 197
  - `backend/internal/handler/gateway_systemone.go` near line 139
  - `backend/internal/handler/openai_gateway_handler.go` near lines 705 and 1334
  - `backend/internal/handler/openai_chat_completions.go` near lines 206 and 223
  - `backend/internal/handler/openai_images.go` near lines 192 and 211
  - `backend/internal/handler/openai_embeddings.go` near lines 161 and 176
  - `backend/internal/handler/openai_alpha_search.go` near line 160
  - `backend/internal/handler/openai_gateway_count_tokens.go` near lines 110, 118, 283, and 291
  - `backend/internal/handler/gemini_v1beta_handler.go` near line 588
  - `backend/internal/handler/grok_media.go` near lines 288 and 308

**Interfaces:**
- Consumes: `service.ErrUserAgentNotAllowed`, `service.UserAgentNotAllowedMessage`
- Produces: `func userAgentNotAllowedResponse(err error) (status int, errType string, message string, ok bool)`

- [ ] **Step 1: Write the failing envelope test**

Append to `backend/internal/handler/no_account_error_test.go`:

```go
func TestUserAgentNotAllowedResponseEnvelopes(t *testing.T) {
	status, errType, message, ok := userAgentNotAllowedResponse(fmt.Errorf("wrap: %w", service.ErrUserAgentNotAllowed))
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, "forbidden_error", errType)
	require.Equal(t, service.UserAgentNotAllowedMessage, message)

	_, _, _, ok = userAgentNotAllowedResponse(service.ErrNoAvailableAccounts)
	require.False(t, ok)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	(&GatewayHandler{}).errorResponse(c, status, errType, message)
	require.JSONEq(t, `{"type":"error","error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}`, w.Body.String())

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	(&OpenAIGatewayHandler{}).errorResponse(c, status, errType, message)
	require.JSONEq(t, `{"error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}`, w.Body.String())
}
```

- [ ] **Step 2: Run the test and confirm failure**

Run: `cd backend && go test -tags unit ./internal/handler/ -run TestUserAgentNotAllowedResponseEnvelopes -count=1`

Expected: FAIL because `userAgentNotAllowedResponse` is undefined.

- [ ] **Step 3: Implement the helper and insert it at every call site**

Add to `no_account_error.go`:

```go
func userAgentNotAllowedResponse(err error) (int, string, string, bool) {
	if !errors.Is(err, service.ErrUserAgentNotAllowed) {
		return 0, "", "", false
	}
	return http.StatusForbidden, "forbidden_error", service.UserAgentNotAllowedMessage, true
}
```

Immediately after each `if err != nil` that currently classifies a selection failure, and before `classifyNoAccountErrorFromGin` or `classifyOpenAICompatibleNoAccountErrorFromGin`, insert:

```go
if status, errType, message, ok := userAgentNotAllowedResponse(err); ok {
	h.handleStreamingAwareError(c, status, errType, message, streamStarted)
	return
}
```

Where the surrounding function uses `h.errorResponse` instead of `handleStreamingAwareError`, call `h.errorResponse(c, status, errType, message)` and return. Do not prefix the message with `No available accounts:`. Do not call `markOpsRoutingCapacityLimitedIfNoAvailable` for this error. Do not run `classifySelectionFailureError` on it.

After editing, run:

```bash
rg -n "classifyNoAccountErrorFromGin|classifyOpenAICompatibleNoAccountErrorFromGin" backend/internal/handler
```

Every hit that handles a live selection error must have `userAgentNotAllowedResponse(err)` earlier in the same error branch. Test-only hits in `no_account_error_test.go` are exempt.

- [ ] **Step 4: Re-run the handler test**

Run: `cd backend && go test -tags unit ./internal/handler/ -run 'TestUserAgentNotAllowedResponseEnvelopes|TestClassifyNoAccountError' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/handler/no_account_error.go backend/internal/handler/no_account_error_test.go backend/internal/handler/gateway_handler.go backend/internal/handler/gateway_handler_chat_completions.go backend/internal/handler/gateway_handler_responses.go backend/internal/handler/gateway_systemone.go backend/internal/handler/openai_gateway_handler.go backend/internal/handler/openai_chat_completions.go backend/internal/handler/openai_images.go backend/internal/handler/openai_embeddings.go backend/internal/handler/openai_alpha_search.go backend/internal/handler/openai_gateway_count_tokens.go backend/internal/handler/gemini_v1beta_handler.go backend/internal/handler/grok_media.go
git commit -m "feat: return 403 when no account accepts the user-agent"
```

---

### Task 8: Admin create, edit, and bulk UI

**Files:**
- Create: `frontend/src/components/account/uaFilter.ts`
- Create: `frontend/src/components/account/uaFilter.test.ts`
- Create: `frontend/src/components/account/UserAgentFilterFields.vue`
- Modify: `frontend/src/components/account/CreateAccountModal.vue`
- Modify: `frontend/src/components/account/EditAccountModal.vue`
- Modify: `frontend/src/components/account/BulkEditAccountModal.vue`
- Modify: `frontend/src/i18n/locales/en/admin/accounts.ts`
- Modify: `frontend/src/i18n/locales/zh/admin/accounts.ts`

**Interfaces:**
- Consumes: the extra keys and preset literals from the Global Constraints
- Produces: `normalizeUAFilter(mode, patterns)`, `presetPatterns`, and a component emit `{ mode: 'off' | 'allow' | 'block' | 'unchanged', patterns: string[] }`

- [ ] **Step 1: Write the failing frontend unit test**

Create `frontend/src/components/account/uaFilter.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { normalizeUAFilter, presetPatterns } from './uaFilter'

describe('normalizeUAFilter', () => {
  it('clears patterns when off', () => {
    expect(normalizeUAFilter('off', ['claude-cli/'])).toEqual({ ok: true, mode: 'off', patterns: [] })
  })

  it('rejects an empty allow list', () => {
    expect(normalizeUAFilter('allow', ['  ']).ok).toBe(false)
  })

  it('dedupes case-insensitively and keeps the first casing', () => {
    const result = normalizeUAFilter('allow', [' Claude-CLI/ ', 'claude-cli/', 'opencode/'])
    expect(result).toEqual({ ok: true, mode: 'allow', patterns: ['Claude-CLI/', 'opencode/'] })
  })

  it('stores codex desktop rather than bare codex', () => {
    expect(presetPatterns.codex).toContain('codex desktop')
    expect(presetPatterns.codex).not.toContain('codex')
    expect(presetPatterns.claudeCode).toEqual(['claude-cli/'])
    expect(presetPatterns.openCode).toEqual(['opencode/'])
  })
})
```

- [ ] **Step 2: Run the test and confirm failure**

Run: `cd frontend && npx vitest run src/components/account/uaFilter.test.ts`

Expected: FAIL because `./uaFilter` does not exist.

- [ ] **Step 3: Implement the shared module and component**

Create `frontend/src/components/account/uaFilter.ts`:

```ts
export type UAFilterMode = 'off' | 'allow' | 'block' | 'unchanged'

export const presetPatterns = {
  claudeCode: ['claude-cli/'],
  codex: [
    'codex_cli_rs/',
    'codex-tui/',
    'codex_vscode/',
    'codex_vscode_copilot/',
    'codex_app/',
    'codex_chatgpt_desktop/',
    'codex_atlas/',
    'codex_exec/',
    'codex_sdk_ts/',
    'codex desktop'
  ],
  openCode: ['opencode/']
} as const

export function normalizeUAFilter(mode: UAFilterMode, patterns: string[]) {
  if (mode === 'off' || mode === 'unchanged') {
    return { ok: true as const, mode, patterns: [] as string[] }
  }
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of patterns) {
    const text = raw.trim()
    if (!text) continue
    const length = [...text].length
    if (length < 3 || length > 128 || /[\u0000-\u001f\u007f]/.test(text)) {
      return { ok: false as const, field: 'ua_filter_patterns' as const }
    }
    const key = text.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(text)
  }
  if (out.length === 0 || out.length > 50) {
    return { ok: false as const, field: 'ua_filter_patterns' as const }
  }
  return { ok: true as const, mode, patterns: out }
}
```

Create `UserAgentFilterFields.vue` with a mode select, three preset checkboxes, a custom pattern input, and the resolved pattern list. Preset checkboxes add or remove that preset's literals from the list. The component does not know about account platform. Bulk mode includes `unchanged`, which is the default when the `bulk` prop is true.

- [ ] **Step 4: Wire the three modals**

Mount the component outside the OpenAI-only `codexCLIOnly` block so every platform sees it.

On create and edit submit, call `normalizeUAFilter`. When `ok` is false, stop submission and show the field error. When mode is `off`, `delete extra.ua_filter_mode` and `delete extra.ua_filter_patterns`. When mode is `allow` or `block`, assign both keys from the normalized result.

On edit load, read those keys back into the component. Missing mode displays as off.

On bulk submit, `unchanged` leaves both keys out of the extra patch. `off`, `allow`, and `block` send the same payload as create. The bulk control defaults to `unchanged` each time the modal opens.

Add English and Chinese strings under `admin.accounts.uaFilter` in both locale files: title, mode labels, preset labels, custom-pattern label, empty-list error, and invalid-pattern error.

- [ ] **Step 5: Re-run the Vitest file and typecheck the touched Vue files**

Run: `cd frontend && npx vitest run src/components/account/uaFilter.test.ts`

Expected: PASS

Run: `cd frontend && pnpm run typecheck`

Expected: PASS. `vue-tsc --noEmit` reports no errors in `UserAgentFilterFields.vue` or the three account modals.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/account/uaFilter.ts frontend/src/components/account/uaFilter.test.ts frontend/src/components/account/UserAgentFilterFields.vue frontend/src/components/account/CreateAccountModal.vue frontend/src/components/account/EditAccountModal.vue frontend/src/components/account/BulkEditAccountModal.vue frontend/src/i18n/locales/en/admin/accounts.ts frontend/src/i18n/locales/zh/admin/accounts.ts
git commit -m "feat: edit per-account user-agent filter in admin ui"
```
