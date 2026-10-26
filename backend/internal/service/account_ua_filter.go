package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	UAFilterExtraModeKey       = "ua_filter_mode"
	UAFilterExtraPatternsKey   = "ua_filter_patterns"
	UAFilterModeOff            = "off"
	UAFilterModeAllow          = "allow"
	UAFilterModeBlock          = "block"
	UAFilterClearOnMergeKey    = "__ua_filter_clear__"
	UserAgentNotAllowedMessage = "No available account accepts this User-Agent"
	uaFilterInvalidMode        = "invalid"
	uaFilterLogMaxBytes        = 256
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
	if len(items) > 50 {
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
