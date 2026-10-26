//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type uaFailoverUpstream struct {
	mu     sync.Mutex
	status int
	calls  []int64
}

func (u *uaFailoverUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	return u.respond(req, accountID)
}

func (u *uaFailoverUpstream) DoWithTLS(req *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.respond(req, accountID)
}

func (u *uaFailoverUpstream) respond(req *http.Request, accountID int64) (*http.Response, error) {
	u.mu.Lock()
	u.calls = append(u.calls, accountID)
	status := u.status
	u.mu.Unlock()
	if status == 0 {
		status = 529
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"overloaded","type":"overloaded_error"}}`)),
		Request:    req,
	}, nil
}

func (u *uaFailoverUpstream) called() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.calls...)
}

func uaFailoverAllowExtra() map[string]any {
	return map[string]any{
		"ua_filter_mode":     "allow",
		"ua_filter_patterns": []string{"claude-cli/"},
	}
}

func requireFailoverNotUserAgentForbidden(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), service.UserAgentNotAllowedMessage)
	require.NotContains(t, rec.Body.String(), "forbidden_error")
}

func newUAFailoverOpenAIHandler(t *testing.T, accounts []service.Account, upstream *uaFailoverUpstream) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	repo := &uaFilterAccountRepoStub{accounts: accounts}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(nil)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		nil, nil, billing, upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	handler := NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	handler.maxAccountSwitches = 3
	return handler
}

func newUAFailoverContext(t *testing.T, method, target, body string, group *service.Group, userAgent string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := service.WithInboundUserAgent(req.Context(), userAgent)
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	c.Request = req.WithContext(ctx)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      9,
		UserID:  4,
		GroupID: &group.ID,
		User:    &service.User{ID: 4, Status: service.StatusActive, Balance: 100},
		Group:   group,
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 4, Concurrency: 0})
	return c, rec
}

func TestUserAgentNotAllowedFailoverOpenAIResponses(t *testing.T) {
	group := &service.Group{ID: 81, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	open := service.Account{
		ID: 1, Name: "open", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"access_token": "token-1"},
	}
	blocked := open
	blocked.ID = 2
	blocked.Name = "blocked"
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"access_token": "token-2"}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 529}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.1","input":"hello"}`, group, "python-requests/2.32")

	h.Responses(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Upstream service overloaded, please retry later")
}

func TestUserAgentNotAllowedFailoverOpenAIMessages(t *testing.T) {
	group := &service.Group{ID: 82, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AllowMessagesDispatch: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-open", "pool_mode": true},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"api_key": "sk-blocked", "pool_mode": true}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, group, "python-requests/2.32")

	h.Messages(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}

func TestUserAgentNotAllowedFailoverOpenAIChatCompletions(t *testing.T) {
	group := &service.Group{ID: 83, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-open", "pool_mode": true},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"api_key": "sk-blocked", "pool_mode": true}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}]}`, group, "python-requests/2.32")

	h.ChatCompletions(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}

func TestUserAgentNotAllowedFailoverOpenAIEmbeddings(t *testing.T) {
	group := &service.Group{ID: 84, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-open", "pool_mode": true},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"api_key": "sk-blocked", "pool_mode": true}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/embeddings", `{"model":"text-embedding-3-small","input":"hi"}`, group, "python-requests/2.32")

	h.Embeddings(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}

func TestUserAgentNotAllowedFailoverOpenAIImages(t *testing.T) {
	group := &service.Group{ID: 85, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, AllowImageGeneration: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"access_token": "token-1"},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"access_token": "token-2"}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/images/generations", `{"model":"gpt-image-1","prompt":"cat"}`, group, "python-requests/2.32")

	h.Images(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}

func TestUserAgentNotAllowedFailoverOpenAIAlphaSearch(t *testing.T) {
	group := &service.Group{ID: 86, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-open", "pool_mode": true},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"api_key": "sk-blocked", "pool_mode": true}
	blocked.Extra = map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"codex_cli_rs/"}}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/alpha/search", `{"model":"gpt-5.1","query":"weather"}`, group, "python-requests/2.32")

	h.AlphaSearch(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}

func newUAFailoverGateway(t *testing.T, group *service.Group, accounts []*service.Account, upstream *uaFailoverUpstream) (*GatewayHandler, service.AccountRepository, *service.SchedulerSnapshotService) {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple, Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{AllowInsecureHTTP: true}}}
	repo := &uaFilterAccountRepoStub{}
	for _, account := range accounts {
		if account != nil {
			repo.accounts = append(repo.accounts, *account)
		}
	}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, cfg)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(&fakeConcurrencyCache{})
	gateway := service.NewGatewayService(
		repo,
		&fakeGroupRepo{group: group},
		nil, nil, nil, nil, nil,
		nil,
		cfg,
		snapshot,
		nil, nil, nil,
		billing,
		nil,
		upstream,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil,
	)
	return &GatewayHandler{
		gatewayService:      gateway,
		billingCacheService: billing,
		concurrencyHelper:   NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0),
		maxAccountSwitches:  3,
		cfg:                 cfg,
	}, repo, snapshot
}

func TestUserAgentNotAllowedFailoverGatewayMessages(t *testing.T) {
	groupID := int64(91)
	group := &service.Group{ID: groupID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-open", "pool_mode": true},
		Extra:         map[string]any{"anthropic_passthrough": true},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blockedExtra := uaFailoverAllowExtra()
	blockedExtra["anthropic_passthrough"] = true
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-blocked", "pool_mode": true},
		Extra:         blockedExtra,
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 529}
	h, _, _ := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/messages", `{"model":"claude-3-5-sonnet-20241022","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, group, "python-requests/2.32")

	h.Messages(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Upstream service overloaded, please retry later")
}

func TestUserAgentNotAllowedFailoverGatewayGeminiMessages(t *testing.T) {
	groupID := int64(92)
	group := &service.Group{ID: groupID, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "gem-open"},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "gem-blocked"},
		Extra:         uaFailoverAllowExtra(),
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 529}
	h, repo, snapshot := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	h.geminiCompatService = service.NewGeminiMessagesCompatService(repo, nil, nil, snapshot, nil, nil, upstream, nil, h.cfg)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/messages", `{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, group, "python-requests/2.32")

	h.Messages(c)

	require.NotEmpty(t, upstream.called(), rec.Body.String())
	for _, id := range upstream.called() {
		require.Equal(t, int64(1), id)
	}
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service overloaded, please retry later")
}

func TestUserAgentNotAllowedFailoverGatewayChatCompletions(t *testing.T) {
	groupID := int64(93)
	group := &service.Group{ID: groupID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-open", "pool_mode": true},
		Extra:         map[string]any{"anthropic_passthrough": true},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blockedExtra := uaFailoverAllowExtra()
	blockedExtra["anthropic_passthrough"] = true
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-blocked", "pool_mode": true},
		Extra:         blockedExtra,
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 529}
	h, _, _ := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/chat/completions", `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hi"}]}`, group, "python-requests/2.32")

	h.ChatCompletions(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "All available accounts exhausted")
}

func TestUserAgentNotAllowedFailoverGatewayResponses(t *testing.T) {
	groupID := int64(94)
	group := &service.Group{ID: groupID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-open", "pool_mode": true},
		Extra:         map[string]any{"anthropic_passthrough": true},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blockedExtra := uaFailoverAllowExtra()
	blockedExtra["anthropic_passthrough"] = true
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "sk-blocked", "pool_mode": true},
		Extra:         blockedExtra,
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 529}
	h, _, _ := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/responses", `{"model":"claude-3-5-sonnet-20241022","input":"hi"}`, group, "python-requests/2.32")

	h.Responses(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "All available accounts exhausted")
}

func TestUserAgentNotAllowedFailoverSystemOne(t *testing.T) {
	groupID := int64(95)
	group := &service.Group{ID: groupID, Platform: service.PlatformTypeSafe, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformTypeSafe, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "ts-open", "base_url": "http://typesafe.test", "pool_mode": true},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformTypeSafe, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "ts-blocked", "base_url": "http://typesafe.test", "pool_mode": true},
		Extra:         uaFailoverAllowExtra(),
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 529}
	h, _, _ := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/systemone", validSystemOneHandlerBody, group, "python-requests/2.32")

	h.SystemOne(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service overloaded, please retry later")
}

func TestUserAgentNotAllowedFailoverWebSearch(t *testing.T) {
	groupID := int64(96)
	group := &service.Group{ID: groupID, Platform: service.PlatformGrok, Status: service.StatusActive, Hydrated: true}
	open := &service.Account{
		ID: 1, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "grok-open"},
		AccountGroups: []service.AccountGroup{{AccountID: 1, GroupID: groupID}},
	}
	blocked := &service.Account{
		ID: 2, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "grok-blocked"},
		Extra:         map[string]any{"ua_filter_mode": "allow", "ua_filter_patterns": []string{"claude-cli/"}},
		AccountGroups: []service.AccountGroup{{AccountID: 2, GroupID: groupID}},
	}
	upstream := &uaFailoverUpstream{status: 503}
	h, _, _ := newUAFailoverGateway(t, group, []*service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/web_search", `{"query":"weather"}`, group, "python-requests/2.32")

	h.WebSearch(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "web_search_error")
}

func TestUserAgentNotAllowedFailoverGrokMedia(t *testing.T) {
	group := &service.Group{ID: 97, Platform: service.PlatformGrok, Status: service.StatusActive, Hydrated: true, AllowImageGeneration: true}
	open := service.Account{
		ID: 1, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 1, Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-open"},
		Extra:       map[string]any{"grok_media_eligible": true},
	}
	blocked := open
	blocked.ID = 2
	blocked.Priority = 2
	blocked.Credentials = map[string]any{"api_key": "grok-blocked"}
	blocked.Extra = map[string]any{
		"grok_media_eligible": true,
		"ua_filter_mode":      "allow",
		"ua_filter_patterns":  []string{"claude-cli/"},
	}
	upstream := &uaFailoverUpstream{status: 503}
	h := newUAFailoverOpenAIHandler(t, []service.Account{open, blocked}, upstream)
	c, rec := newUAFailoverContext(t, http.MethodPost, "/v1/images/generations", `{"model":"grok-imagine-image","prompt":"cat"}`, group, "python-requests/2.32")

	h.GrokImages(c)

	require.Equal(t, []int64{1}, upstream.called(), rec.Body.String())
	requireFailoverNotUserAgentForbidden(t, rec)
	require.Contains(t, rec.Body.String(), "Upstream service temporarily unavailable")
}
