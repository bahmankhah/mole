package handlers

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// TokenAuth protects the /v1 API with bearer tokens. With no tokens configured
// the API is open, which is only appropriate on a private network.
func TokenAuth(tokens []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(tokens) == 0 {
			c.Next()
			return
		}
		auth := c.GetHeader("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			given := []byte(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
			for _, t := range tokens {
				if subtle.ConstantTimeCompare(given, []byte(t)) == 1 {
					c.Next()
					return
				}
			}
		}
		c.Header("WWW-Authenticate", `Bearer realm="mole"`)
		abortV1(c, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
	}
}

// BasicAuth protects the web UI and its /api endpoints when a username is set.
func BasicAuth(username, password string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if username == "" {
			c.Next()
			return
		}
		u, p, ok := c.Request.BasicAuth()
		if ok && subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1 &&
			subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1 {
			c.Next()
			return
		}
		c.Header("WWW-Authenticate", `Basic realm="Mole", charset="UTF-8"`)
		c.AbortWithStatus(http.StatusUnauthorized)
	}
}
