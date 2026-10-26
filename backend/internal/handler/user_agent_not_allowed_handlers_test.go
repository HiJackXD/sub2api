//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
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
