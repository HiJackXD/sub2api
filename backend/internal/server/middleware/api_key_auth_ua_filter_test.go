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
