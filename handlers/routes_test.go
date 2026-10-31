package handlers

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/config"
)

// TestRegisterRoutes guards the route tree: gin panics at registration time
// when two paths conflict (a static segment against a wildcard at the same
// position), which would otherwise only show up when the server starts.
func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering routes panicked: %v", r)
		}
	}()
	cfg := config.DefaultConfig()
	cfg.UI.Enabled = true
	// The handlers are only invoked per request, so a Handler with no job
	// manager is enough to build the route tree.
	Register(gin.New(), &Handler{}, cfg)
}

// TestRegisterRoutesUIDisabled covers the other branch of Register.
func TestRegisterRoutesUIDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering routes panicked: %v", r)
		}
	}()
	cfg := config.DefaultConfig()
	cfg.UI.Enabled = false
	Register(gin.New(), &Handler{}, cfg)
}
