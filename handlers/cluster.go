package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/cluster"
	"github.com/resolver/crawler/jobs"
	"github.com/resolver/crawler/models"
	"github.com/resolver/crawler/plugins"
	"gopkg.in/yaml.v3"
)

const ctxJoinToken = "mole.joinToken"

// RegisterCluster mounts the endpoints other machines use to join this server
// as workers. They are authenticated with join tokens, not API tokens, and are
// available with and without the web UI.
func (h *Handler) RegisterCluster(router *gin.Engine) {
	router.GET("/cluster/install.sh", h.ClusterInstallScript)
	g := router.Group("/cluster", h.joinTokenAuth())
	g.POST("/join", h.ClusterJoin)
	g.GET("/download/mole", h.ClusterBinary)
	g.GET("/download/plugins.tar.gz", h.ClusterPlugins)
}

func (h *Handler) joinTokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			abortV1(c, http.StatusUnauthorized, "unauthorized", "missing join token (Authorization: Bearer mjt_…)")
			return
		}
		// Only joining counts as a use; downloads just check the token.
		record := c.Request.Method == http.MethodPost
		tok, err := h.jobManager.CheckJoinToken(strings.TrimPrefix(auth, "Bearer "), record)
		if err != nil {
			if errors.Is(err, jobs.ErrJoinTokenInvalid) {
				abortV1(c, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			v1Error(c, err)
			return
		}
		c.Set(ctxJoinToken, tok)
		c.Next()
	}
}

// controlURL is the URL nodes use to reach this server: cluster.advertise_url,
// or else the URL of the current request.
func (h *Handler) controlURL(c *gin.Context) (string, error) {
	if u := h.jobManager.Config().Cluster.AdvertiseURL; u != "" {
		return cluster.ValidateControlURL(u, true)
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := c.Request.Host
	if fh := c.GetHeader("X-Forwarded-Host"); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	return cluster.ValidateControlURL(scheme+"://"+host, true)
}

// ClusterInstallScript serves the node installer. It holds no secrets: the
// join token is passed to it on the command line.
func (h *Handler) ClusterInstallScript(c *gin.Context) {
	u, err := h.controlURL(c)
	if err != nil {
		c.String(http.StatusBadRequest, "cannot work out this server's URL (%v); set cluster.advertise_url\n", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(cluster.InstallScript(u)))
}

// ClusterJoin returns the configuration of a joining (or restarting) node.
func (h *Handler) ClusterJoin(c *gin.Context) {
	// Decoded leniently: a node of another version may send other fields, and
	// should get the version warning rather than a decoding error.
	var req cluster.JoinRequest
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 64<<10)).Decode(&req); err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	tok := c.MustGet(ctxJoinToken).(*models.JoinToken)
	nodeCfg, warnings, err := cluster.NodeConfig(h.jobManager.Config(), tok.Region)
	if err != nil {
		if errors.Is(err, cluster.ErrLocalStorage) {
			abortV1(c, http.StatusConflict, "storage_not_shared", err.Error())
			return
		}
		v1Error(c, err)
		return
	}
	out, err := yaml.Marshal(nodeCfg)
	if err != nil {
		v1Error(c, err)
		return
	}
	if req.Version != "" && req.Version != jobs.Version {
		warnings = append(warnings, "this node runs mole "+req.Version+" but the control node runs "+jobs.Version)
	}
	log.Printf("[Cluster] Node %q (host %q, mole %s, %s/%s) fetched its configuration with join token %s",
		req.WorkerID, req.Hostname, req.Version, req.OS, req.Arch, tok.Prefix)
	ok(c, http.StatusOK, cluster.JoinResponse{
		Version:  jobs.Version,
		Config:   string(out),
		Region:   tok.Region,
		Warnings: warnings,
	})
}

// ClusterBinary serves this server's own executable so nodes run the same version.
func (h *Handler) ClusterBinary(c *gin.Context) {
	exe, err := os.Executable()
	if err != nil {
		v1Error(c, err)
		return
	}
	c.Header("X-Mole-Version", jobs.Version)
	c.Header("X-Mole-Platform", runtime.GOOS+"/"+runtime.GOARCH)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="mole"`)
	c.File(exe)
}

// ClusterPlugins serves the plugins directory (browser script, requirements,
// operator plugins) as a .tar.gz.
func (h *Handler) ClusterPlugins(c *gin.Context) {
	dir, err := plugins.ResolveDir()
	if err != nil {
		abortV1(c, http.StatusNotFound, "not_found", err.Error())
		return
	}
	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", `attachment; filename="plugins.tar.gz"`)
	c.Status(http.StatusOK)
	if err := cluster.WritePluginBundle(c.Writer, dir); err != nil {
		// Headers are sent; the truncated archive makes the installer fail.
		log.Printf("[Cluster] Plugin bundle failed: %v", err)
	}
}

// ListJoinTokens lists join tokens (UI).
func (h *Handler) ListJoinTokens(c *gin.Context) {
	list, err := h.jobManager.ListJoinTokens()
	if err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, list)
}

// CreateJoinToken creates a join token and returns it with the commands that
// add a node with it (UI).
func (h *Handler) CreateJoinToken(c *gin.Context) {
	var body struct {
		Name     string  `json:"name"`
		Region   string  `json:"region"`
		TTLHours float64 `json:"ttl_hours"` // 0 = never expires
	}
	if _, okBody := bindJSON(c, &body); !okBody {
		return
	}
	if body.TTLHours < 0 || body.TTLHours > 24*3650 {
		abortV1(c, http.StatusBadRequest, "invalid_request", "ttl_hours must be between 0 (never expires) and 87600")
		return
	}
	u, err := h.controlURL(c)
	if err != nil {
		abortV1(c, http.StatusBadRequest, "invalid_request", "cannot work out this server's URL ("+err.Error()+"); set cluster.advertise_url")
		return
	}
	tok, secret, err := h.jobManager.CreateJoinToken(jobs.JoinTokenSpec{
		Name: body.Name, Region: body.Region, TTL: time.Duration(body.TTLHours * float64(time.Hour)),
	})
	if err != nil {
		v1Error(c, err)
		return
	}
	install, join, docker := cluster.JoinCommands(u, secret)
	var warnings []string
	if _, w, err := cluster.NodeConfig(h.jobManager.Config(), tok.Region); err != nil {
		warnings = append(warnings, err.Error())
	} else {
		warnings = append(warnings, w...)
	}
	ok(c, http.StatusCreated, gin.H{
		"token": tok, "secret": secret, "control_url": u,
		"install_command": install, "join_command": join, "docker_command": docker,
		"warnings": warnings,
	})
}

// RevokeJoinToken revokes a join token (UI).
func (h *Handler) RevokeJoinToken(c *gin.Context) {
	if err := h.jobManager.RevokeJoinToken(c.Param("id")); err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"revoked": true})
}

// SetWorkerState cordons, drains or reactivates a worker (UI).
func (h *Handler) SetWorkerState(c *gin.Context) {
	var body struct {
		State string `json:"state"`
	}
	if _, okBody := bindJSON(c, &body); !okBody {
		return
	}
	if err := h.jobManager.SetWorkerState(c.Param("id"), body.State); err != nil {
		v1Error(c, err)
		return
	}
	ok(c, http.StatusOK, gin.H{"worker_id": c.Param("id"), "state": body.State})
}
