//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const userAgentNotAllowedGoogleBody = `{"error":{"code":403,"message":"No available account accepts this User-Agent","status":"PERMISSION_DENIED"}}`
const userAgentNotAllowedOpenAIBody = `{"error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}`

type uaFilterAccountRepoStub struct {
	service.AccountRepository
	accounts []service.Account
}

func (s *uaFilterAccountRepoStub) ListSchedulableByGroupIDAndPlatforms(_ context.Context, _ int64, platforms []string) ([]service.Account, error) {
	return s.matching(platforms), nil
}

func (s *uaFilterAccountRepoStub) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return s.matching([]string{platform}), nil
}

func (s *uaFilterAccountRepoStub) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return s.matching([]string{platform}), nil
}

func (s *uaFilterAccountRepoStub) matching(platforms []string) []service.Account {
	allowed := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		allowed[platform] = struct{}{}
	}
	out := make([]service.Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if _, ok := allowed[account.Platform]; ok {
			out = append(out, account)
		}
	}
	return out
}

func uaBlockedGeminiAccount() service.Account {
	return service.Account{
		ID:          11,
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":                 "gemini-key",
			"explicit_cache_upstream": true,
		},
		Extra: map[string]any{
			"ua_filter_mode":     "allow",
			"ua_filter_patterns": []string{"claude-cli/"},
		},
	}
}

func uaBlockedGrokAccount() service.Account {
	return service.Account{
		ID:          21,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-key"},
		Extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []string{"python-requests"},
		},
	}
}

func newSimpleBillingForUATest(t *testing.T) *service.BillingCacheService {
	t.Helper()
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, &config.Config{RunMode: config.RunModeSimple}, nil)
	t.Cleanup(billing.Stop)
	return billing
}

func newUAGatewayService(repo service.AccountRepository) *service.GatewayService {
	return service.NewGatewayService(
		repo,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func requireUserAgentForbiddenGoogle(t *testing.T, rec *httptest.ResponseRecorder, c *gin.Context) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, userAgentNotAllowedGoogleBody, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "No available Gemini accounts:")
	errType, code := service.OpsLocalErrorType(c)
	require.Equal(t, "forbidden_error", errType)
	require.Empty(t, code)
	require.False(t, isOpsRoutingCapacityLimited(c))
}

func TestGeminiV1BetaListModels_UserAgentNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(41)
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedGeminiAccount()}}
	h := &GatewayHandler{geminiCompatService: service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, nil, nil, nil)}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	c.Request = req.WithContext(service.WithInboundUserAgent(req.Context(), ""))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})

	h.GeminiV1BetaListModels(c)
	requireUserAgentForbiddenGoogle(t, rec, c)
}

func TestGeminiV1BetaGetModel_UserAgentNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(41)
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedGeminiAccount()}}
	h := &GatewayHandler{geminiCompatService: service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, nil, nil, nil)}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-2.5-flash", nil)
	c.Request = req.WithContext(service.WithInboundUserAgent(req.Context(), ""))
	c.Params = gin.Params{{Key: "model", Value: "gemini-2.5-flash"}}
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})

	h.GeminiV1BetaGetModel(c)
	requireUserAgentForbiddenGoogle(t, rec, c)
}

func TestWebSearch_UserAgentNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(77)
	group := &service.Group{ID: groupID, Platform: service.PlatformGrok, Status: service.StatusActive, Hydrated: true}
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedGrokAccount()}}
	h := &GatewayHandler{
		gatewayService:      newUAGatewayService(repo),
		billingCacheService: newSimpleBillingForUATest(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/web_search", bytes.NewBufferString(`{"query":"weather"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := service.WithInboundUserAgent(req.Context(), "python-requests/2")
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	c.Request = req.WithContext(ctx)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      7,
		GroupID: &groupID,
		User:    &service.User{ID: 3, Status: service.StatusActive},
		Group:   group,
	})

	h.WebSearch(c)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
}

func TestGeminiCachedContentsCreate_UserAgentNotAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(42)
	group := &service.Group{ID: groupID, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true}
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedGeminiAccount()}}
	h := &GatewayHandler{
		gatewayService:             newUAGatewayService(repo),
		billingCacheService:        newSimpleBillingForUATest(t),
		geminiCachedContentService: service.NewGeminiCachedContentService(&geminiCachedContentHandlerRepoStub{}, nil, nil),
		concurrencyHelper:          NewConcurrencyHelper(service.NewConcurrencyService(nil), SSEPingFormatNone, 0),
	}
	apiKey := &service.APIKey{
		ID:      7,
		UserID:  3,
		GroupID: &groupID,
		User:    &service.User{ID: 3, Status: service.StatusActive},
		Group:   group,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/cachedContents", bytes.NewBufferString(`{"model":"models/gemini-2.5-flash"}`))
	ctx := service.WithInboundUserAgent(req.Context(), "")
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	c.Request = req.WithContext(ctx)
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 0})

	h.GeminiCachedContentsCreate(c)
	requireUserAgentForbiddenGoogle(t, rec, c)
}

func newUAGrokVoiceHandler(t *testing.T) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := newSimpleBillingForUATest(t)
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedGrokAccount()}}
	concurrency := service.NewConcurrencyService(nil)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
		nil, nil, billing, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
}

func newUAGrokAudioContext(userAgent, method, target string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	groupID := int64(77)
	group := &service.Group{ID: groupID, Platform: service.PlatformGrok, Status: service.StatusActive, Hydrated: true}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := service.WithInboundUserAgent(req.Context(), userAgent)
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	c.Request = req.WithContext(ctx)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      9,
		GroupID: &groupID,
		User:    &service.User{ID: 4, Status: service.StatusActive},
		Group:   group,
	})
	return c, rec
}

func TestGrokVoice_UserAgentNotAllowed(t *testing.T) {
	h := newUAGrokVoiceHandler(t)
	c, rec := newUAGrokAudioContext("python-requests/2", http.MethodPost, "/v1/tts", `{"input":"hello"}`)

	h.GrokVoice(c, "stt")

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
}

func TestGrokRealtime_UserAgentNotAllowed(t *testing.T) {
	h := newUAGrokVoiceHandler(t)
	c, rec := newUAGrokAudioContext("python-requests/2", http.MethodGet, "/v1/realtime", "")
	c.Request.Header.Set("Upgrade", "websocket")
	c.Request.Header.Set("Connection", "upgrade")

	h.GrokRealtime(c)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
}

func uaBlockedAnthropicAccount() service.Account {
	return service.Account{
		ID:          31,
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "sk-ant"},
		Extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []string{"python-requests"},
		},
	}
}

func uaBlockedOpenAIAccount() service.Account {
	return service.Account{
		ID:          41,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "sk-openai"},
		Extra: map[string]any{
			"ua_filter_mode":     "block",
			"ua_filter_patterns": []string{"python-requests"},
		},
	}
}

func uaAllowedGrokAccount() service.Account {
	return service.Account{
		ID:          22,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "grok-allowed"},
	}
}

func requireUserAgentForbiddenOpenAI(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
	require.Equal(t, "forbidden_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, service.UserAgentNotAllowedMessage, gjson.Get(rec.Body.String(), "error.message").String())
	require.NotContains(t, rec.Body.String(), "ua_filter")
}

func TestUserAgentNotAllowedResponsesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(81)
	group := &service.Group{ID: groupID, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedAnthropicAccount()}}
	h := &GatewayHandler{
		gatewayService:      newUAGatewayService(repo),
		billingCacheService: newSimpleBillingForUATest(t),
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(nil), SSEPingFormatNone, 0),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"claude-3-5-sonnet","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "python-requests/2")
	ctx := service.WithInboundUserAgent(req.Context(), "python-requests/2")
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	c.Request = req.WithContext(ctx)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      8,
		UserID:  3,
		GroupID: &groupID,
		User:    &service.User{ID: 3, Status: service.StatusActive},
		Group:   group,
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 3, Concurrency: 0})

	h.Responses(c)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, "forbidden_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "forbidden_error", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, service.UserAgentNotAllowedMessage, gjson.Get(rec.Body.String(), "error.message").String())
	require.NotContains(t, rec.Body.String(), "No available accounts:")
	require.NotContains(t, rec.Body.String(), "ua_filter")
}

func TestUserAgentNotAllowedResponsesWebSocketBeforeDial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := newSimpleBillingForUATest(t)
	repo := &uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedOpenAIAccount()}}
	concurrency := service.NewConcurrencyService(nil)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
		nil, nil, billing, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)

	groupID := int64(82)
	group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	apiKey := &service.APIKey{
		ID:      82,
		GroupID: &groupID,
		User:    &service.User{ID: 8, Status: service.StatusActive},
		Group:   group,
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 0})
		ctx := service.WithInboundUserAgent(c.Request.Context(), c.GetHeader("User-Agent"))
		ctx = context.WithValue(ctx, ctxkey.Group, group)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	headers := http.Header{}
	headers.Set("User-Agent", "python-requests/2")
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses",
		&coderws.DialOptions{HTTPHeader: headers},
	)
	cancelDial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.CloseNow() })

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1"}`))
	cancelWrite()
	require.NoError(t, err)

	var frames [][]byte
	var closeErr coderws.CloseError
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, cancelRead := context.WithTimeout(context.Background(), time.Until(deadline))
		_, payload, readErr := clientConn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			require.ErrorAs(t, readErr, &closeErr)
			break
		}
		frames = append(frames, payload)
	}
	require.NotEmpty(t, frames)
	body := string(frames[0])
	require.Equal(t, "error", gjson.Get(body, "type").String())
	require.Equal(t, "forbidden_error", gjson.Get(body, "error.type").String())
	require.Equal(t, service.UserAgentNotAllowedMessage, gjson.Get(body, "error.message").String())
	require.NotContains(t, body, "ua_filter")
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Contains(t, closeErr.Reason, service.UserAgentNotAllowedMessage)
}

func TestUserAgentNotAllowedLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)

	(&OpenAIGatewayHandler{}).writeLiveCreateError(c, fmt.Errorf("select: %w", service.ErrUserAgentNotAllowed))

	requireUserAgentForbiddenOpenAI(t, rec)
}

func TestUserAgentNotAllowedCodexModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(83)
	group := &service.Group{
		ID:       groupID,
		Platform: service.PlatformOpenAI,
		Status:   service.StatusActive,
		Hydrated: true,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{
			Enabled:             true,
			FallbackToScheduler: true,
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gateway := service.NewOpenAIGatewayService(
		&uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedOpenAIAccount()}},
		nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{gatewayService: gateway}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.144.0", nil)
	c.Request = req.WithContext(service.WithInboundUserAgent(req.Context(), "python-requests/2"))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: group})

	h.CodexModels(c)
	requireUserAgentForbiddenOpenAI(t, rec)
}

func TestUserAgentNotAllowedPinnedOpenAIModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(84)
	group := &service.Group{
		ID:       groupID,
		Platform: service.PlatformOpenAI,
		Status:   service.StatusActive,
		Hydrated: true,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{
			Enabled:             true,
			FallbackToScheduler: true,
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gateway := service.NewOpenAIGatewayService(
		&uaFilterAccountRepoStub{accounts: []service.Account{uaBlockedOpenAIAccount()}},
		nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{openAIGatewayService: gateway, maxAccountSwitches: 3}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Request = req.WithContext(service.WithInboundUserAgent(req.Context(), "python-requests/2"))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: group})

	h.Models(c)
	requireUserAgentForbiddenOpenAI(t, rec)
}

type uaFilterStatusUpstream struct {
	status int
	calls  int
}

func (u *uaFilterStatusUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: u.status,
		Status:     http.StatusText(u.status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream unavailable","type":"upstream_error"}}`)),
		Request:    req,
	}, nil
}

func (u *uaFilterStatusUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type uaFilterTempUnschedRepo struct {
	uaFilterAccountRepoStub
}

func (uaFilterTempUnschedRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func TestUserAgentNotAllowedGrokVoiceAfterUpstreamFailure(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := newSimpleBillingForUATest(t)
	upstream := &uaFilterStatusUpstream{status: http.StatusBadGateway}
	repo := &uaFilterTempUnschedRepo{uaFilterAccountRepoStub: uaFilterAccountRepoStub{accounts: []service.Account{
		uaAllowedGrokAccount(),
		uaBlockedGrokAccount(),
	}}}
	concurrency := service.NewConcurrencyService(nil)
	gateway := service.NewOpenAIGatewayService(
		repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
		nil, nil, billing, upstream,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	c, rec := newUAGrokAudioContext("python-requests/2", http.MethodPost, "/v1/tts", `{"input":"hello"}`)

	h.GrokVoice(c, "stt")

	require.Equal(t, 1, upstream.calls)
	require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), service.UserAgentNotAllowedMessage)
	require.NotContains(t, rec.Body.String(), "forbidden_error")
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

func uaCredentiallessGrokAccount() service.Account {
	return service.Account{
		ID:          23,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{},
	}
}

// uaProfitFlipSchedulerCache returns a passing rate on the first read of the
// allowed account and a veto rate afterwards. Selection admits the account;
// the post-slot recheck then continues without opening the upstream.
type uaProfitFlipSchedulerCache struct {
	fakeSchedulerCache
	allowedID   int64
	allowedGets int
	cheap       *service.Account
	expensive   *service.Account
	blocked     *service.Account
}

func (c *uaProfitFlipSchedulerCache) GetSnapshot(context.Context, service.SchedulerBucket) ([]*service.Account, bool, error) {
	return []*service.Account{c.cheap, c.blocked}, true, nil
}

func (c *uaProfitFlipSchedulerCache) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if c.blocked != nil && id == c.blocked.ID {
		clone := *c.blocked
		return &clone, nil
	}
	if id != c.allowedID {
		return nil, nil
	}
	c.allowedGets++
	src := c.cheap
	if c.allowedGets > 1 {
		src = c.expensive
	}
	if src == nil {
		return nil, nil
	}
	clone := *src
	return &clone, nil
}

func TestUserAgentNotAllowedGrokAfterUnprobedContinue(t *testing.T) {
	t.Run("realtime credential", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		billing := newSimpleBillingForUATest(t)
		upstream := &uaFilterStatusUpstream{status: http.StatusBadGateway}
		repo := &uaFilterAccountRepoStub{accounts: []service.Account{
			uaCredentiallessGrokAccount(),
			uaBlockedGrokAccount(),
		}}
		concurrency := service.NewConcurrencyService(nil)
		gateway := service.NewOpenAIGatewayService(
			repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
			nil, nil, billing, upstream,
			nil, nil, nil, nil, nil, nil, nil, nil,
		)
		h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
		c, rec := newUAGrokAudioContext("python-requests/2", http.MethodGet, "/v1/realtime", "")
		c.Request.Header.Set("Upgrade", "websocket")
		c.Request.Header.Set("Connection", "upgrade")

		h.GrokRealtime(c)

		require.Zero(t, upstream.calls)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
		require.Equal(t, service.UserAgentNotAllowedMessage, gjson.Get(rec.Body.String(), "error.message").String())
	})

	t.Run("voice slot", func(t *testing.T) {
		cfg := &config.Config{RunMode: config.RunModeSimple}
		billing := newSimpleBillingForUATest(t)
		upstream := &uaFilterStatusUpstream{status: http.StatusBadGateway}
		cheapRate := 0.1
		expensiveRate := 0.9
		cheap := uaAllowedGrokAccount()
		cheap.RateMultiplier = &cheapRate
		expensive := cheap
		expensive.RateMultiplier = &expensiveRate
		expensive.UpdatedAt = time.Now()
		blocked := uaBlockedGrokAccount()
		cache := &uaProfitFlipSchedulerCache{
			allowedID: cheap.ID,
			cheap:     &cheap,
			expensive: &expensive,
			blocked:   &blocked,
		}
		snapshot := service.NewSchedulerSnapshotService(cache, nil, nil, nil, cfg)
		concurrency := service.NewConcurrencyService(nil)
		gateway := service.NewOpenAIGatewayService(
			nil, nil, nil, nil, nil, nil, nil, cfg, snapshot, concurrency,
			nil, nil, billing, upstream,
			nil, nil, nil, nil, nil, nil, nil, nil,
		)
		h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)

		groupID := int64(77)
		group := &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformGrok,
			Status:               service.StatusActive,
			Hydrated:             true,
			RateMultiplier:       1,
			ProfitControlEnabled: true,
			ProfitMinMargin:      0.5,
		}
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodPost, "/v1/tts", bytes.NewBufferString(`{"input":"hello"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := service.WithInboundUserAgent(req.Context(), "python-requests/2")
		ctx = context.WithValue(ctx, ctxkey.Group, group)
		c.Request = req.WithContext(ctx)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID:      9,
			GroupID: &groupID,
			User:    &service.User{ID: 4, Status: service.StatusActive},
			Group:   group,
		})

		h.GrokVoice(c, "stt")

		require.GreaterOrEqual(t, cache.allowedGets, 2)
		require.Zero(t, upstream.calls)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		require.JSONEq(t, userAgentNotAllowedOpenAIBody, rec.Body.String())
		require.Equal(t, service.UserAgentNotAllowedMessage, gjson.Get(rec.Body.String(), "error.message").String())
	})
}
