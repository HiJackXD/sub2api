//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

func TestApiKeyAuthWithSubscriptionGoogleSetsInboundUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	group := &service.Group{
		ID:       99,
		Name:     "g1",
		Status:   service.StatusActive,
		Platform: service.PlatformGemini,
		Hydrated: true,
	}
	user := &service.User{
		ID:          7,
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	apiKey := &service.APIKey{
		ID:     100,
		UserID: user.ID,
		Key:    "test-key",
		Status: service.StatusActive,
		User:   user,
		Group:  group,
	}
	apiKey.GroupID = &group.ID

	apiKeyService := service.NewAPIKeyService(
		fakeAPIKeyRepo{
			getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
				if key != apiKey.Key {
					return nil, service.ErrAPIKeyNotFound
				}
				clone := *apiKey
				return &clone, nil
			},
		},
		nil,
		nil,
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeSimple},
	)

	cfg := &config.Config{RunMode: config.RunModeSimple}
	r := gin.New()
	r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, cfg))

	t.Run("missing User-Agent", func(t *testing.T) {
		var ua string
		var enforced bool
		r.GET("/v1beta/ua-missing", func(c *gin.Context) {
			ua, enforced = service.InboundUserAgentFromContext(c.Request.Context())
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/v1beta/ua-missing", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, enforced)
		require.Empty(t, ua)
	})

	t.Run("User-Agent claude-cli/2.1.1", func(t *testing.T) {
		var ua string
		var enforced bool
		r.GET("/v1beta/ua-present", func(c *gin.Context) {
			ua, enforced = service.InboundUserAgentFromContext(c.Request.Context())
			c.Status(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/v1beta/ua-present", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		req.Header.Set("User-Agent", "claude-cli/2.1.1")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, enforced)
		require.Equal(t, "claude-cli/2.1.1", ua)
	})
}
