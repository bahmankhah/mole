// Package cluster lets other machines join a Mole control node as workers:
// the control node hands each node its configuration, binary and plugins, and
// a joined node refreshes its configuration from the control node on start.
package cluster

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/resolver/crawler/config"
	"gopkg.in/yaml.v3"
)

// ErrLocalStorage means the control node keeps screenshots and offloaded page
// bodies on its own disk, where other machines cannot read or write them.
var ErrLocalStorage = errors.New("storage.driver is local: screenshots and offloaded page bodies written by other machines would be lost. " +
	"Use S3-compatible storage (storage.driver: s3, e.g. MinIO) or set cluster.allow_local_storage: true")

// JoinRequest is what a node sends when it joins or syncs its configuration.
type JoinRequest struct {
	WorkerID string `json:"worker_id"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// JoinResponse is the control node's answer to a JoinRequest.
type JoinResponse struct {
	Version  string   `json:"version"` // the control node's version
	Config   string   `json:"config"`  // the node's configuration as YAML
	Region   string   `json:"region"`  // region of the join token ("" = any)
	Warnings []string `json:"warnings,omitempty"`
}

// NodeConfig derives the configuration a node runs with from the control
// node's effective configuration: the same database, storage, network, LLM,
// crawler, webhook and plugin settings, without the control node's server, API
// and UI settings or machine-specific paths.
func NodeConfig(control *config.Config, region string) (*config.Config, []string, error) {
	if control.Storage.Driver == "local" && !control.Cluster.AllowLocalStorage {
		return nil, nil, ErrLocalStorage
	}
	n, err := clone(control)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	cl := control.Cluster
	if cl.NodeDatabaseHost != "" {
		n.Database.Host = cl.NodeDatabaseHost
	}
	if cl.NodeDatabasePort != "" {
		n.Database.Port = cl.NodeDatabasePort
	}
	if isLoopback(n.Database.Host) {
		warnings = append(warnings, fmt.Sprintf("the database host is %s, which other machines cannot reach; set cluster.node_database_host on the control node (or DB_HOST on this node)", n.Database.Host))
	}
	if n.Storage.Driver == "s3" {
		if cl.NodeS3Endpoint != "" {
			n.Storage.S3.Endpoint = cl.NodeS3Endpoint
		}
		host := n.Storage.S3.Endpoint
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if isLoopback(host) {
			warnings = append(warnings, fmt.Sprintf("the S3 endpoint is %s, which other machines cannot reach; set cluster.node_s3_endpoint on the control node", n.Storage.S3.Endpoint))
		}
	}

	defaults := config.DefaultConfig()
	n.Server = defaults.Server
	n.API = config.APIConfig{}
	n.UI = config.UIConfig{}
	n.Worker.ID = ""
	n.Worker.Region = region
	n.Worker.Browser = nil
	// Paths on the control machine mean nothing on the node; it finds its own.
	n.Crawler.PythonPath = ""
	n.Crawler.HeadlessScriptPath = ""
	n.Crawler.EmbeddingScriptPath = ""
	// Workers never serve /v1/search; keep the provider key on the control node.
	n.Search = defaults.Search
	n.Cluster = config.ClusterConfig{}
	return n, warnings, nil
}

// NodeFile renders the configuration file of a joined node: the control
// node's configuration plus the node's own settings and sync credentials.
func NodeFile(resp *JoinResponse, controlURL, token string, node config.NodeSettings) ([]byte, error) {
	cfg := &config.Config{}
	if err := yaml.Unmarshal([]byte(resp.Config), cfg); err != nil {
		return nil, fmt.Errorf("control node sent an invalid configuration: %w", err)
	}
	cfg.Cluster = config.ClusterConfig{ControlURL: controlURL, JoinToken: token, Node: node}
	cfg.ApplyNode()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("control node sent an invalid configuration: %w", err)
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	header := "# Managed by \"mole join\": this node refreshes the file from " + controlURL + " each time\n" +
		"# \"mole worker\" starts. Change this node's own values under cluster.node (or re-run\n" +
		"# \"mole join\" with flags); edit everything else on the control node.\n"
	return append([]byte(header), body...), nil
}

// IsNodeFile reports whether the file at path is a joined node's configuration.
func IsNodeFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var raw struct {
		Cluster struct {
			ControlURL string `yaml:"control_url"`
		} `yaml:"cluster"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, nil
	}
	return raw.Cluster.ControlURL != "", nil
}

// ReadNodeSettings returns the cluster.node section of a joined node's file.
func ReadNodeSettings(path string) (config.NodeSettings, error) {
	var raw struct {
		Cluster struct {
			Node config.NodeSettings `yaml:"node"`
		} `yaml:"cluster"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return raw.Cluster.Node, err
	}
	err = yaml.Unmarshal(data, &raw)
	return raw.Cluster.Node, err
}

// WriteFileAtomic replaces path with data (mode 0600), so a crash never leaves
// a half-written configuration behind.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mole-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func clone(c *config.Config) (*config.Config, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	out := &config.Config{}
	if err := yaml.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

func isLoopback(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
