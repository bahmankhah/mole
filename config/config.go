package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the crawler
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	Crawler   CrawlerConfig   `yaml:"crawler"`
	Server    ServerConfig    `yaml:"server"`
	Subdomain SubdomainConfig `yaml:"subdomain"`
	PortScan  PortScanConfig  `yaml:"port_scan"`
	API       APIConfig       `yaml:"api"`
	UI        UIConfig        `yaml:"ui"`
	Worker    WorkerConfig    `yaml:"worker"`
	Security  SecurityConfig  `yaml:"security"`
	Network   NetworkConfig   `yaml:"network"`
	Headless  HeadlessConfig  `yaml:"headless"`
	Storage   StorageConfig   `yaml:"storage"`
	LLM       LLMConfig       `yaml:"llm"`
	Webhooks  WebhooksConfig  `yaml:"webhooks"`
	Plugins   PluginsConfig   `yaml:"plugins"`
	Cluster   ClusterConfig   `yaml:"cluster"`
	Documents DocumentsConfig `yaml:"documents"`
	Cache     CacheConfig     `yaml:"cache"`
	Search    SearchConfig    `yaml:"search"`
}

// DocumentsConfig controls how PDF and DOCX responses are converted to text.
type DocumentsConfig struct {
	MaxPages int           `yaml:"max_pages"` // PDF pages converted per document; later pages are ignored
	Timeout  time.Duration `yaml:"timeout"`   // per document conversion
}

// CacheConfig controls the scrape result cache used by /v1/scrape max_age.
type CacheConfig struct {
	Enabled       bool          `yaml:"enabled"`
	TTL           time.Duration `yaml:"ttl"`             // entries older than this are deleted
	MaxEntryBytes int           `yaml:"max_entry_bytes"` // larger results (compressed) are not cached
}

// SearchConfig selects the web search provider behind /v1/search.
type SearchConfig struct {
	Provider       string        `yaml:"provider"` // "", searxng, brave or serper
	BaseURL        string        `yaml:"base_url"` // searxng instance URL
	APIKey         string        `yaml:"api_key"`  // brave / serper key
	Proxy          string        `yaml:"proxy"`    // optional proxy URL for provider requests
	Timeout        time.Duration `yaml:"timeout"`
	DefaultLang    string        `yaml:"default_lang"`    // used when a request names no language
	DefaultCountry string        `yaml:"default_country"` // used when a request names no country
}

// APIConfig secures the versioned /v1 API used by external control planes.
type APIConfig struct {
	Tokens []string `yaml:"tokens"` // bearer tokens accepted by /v1; empty = open (local use only)
}

// UIConfig controls the built-in web UI and its /api endpoints.
type UIConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"` // optional HTTP basic auth for the UI
	Password string `yaml:"password"`
}

// WorkerConfig controls how this process claims and runs queued jobs.
type WorkerConfig struct {
	ID                 string        `yaml:"id"`                    // unique worker id; default hostname-pid
	Region             string        `yaml:"region"`                // e.g. "ir" or "intl"; jobs pinned to another region are skipped
	MaxRunningJobs     int           `yaml:"max_running_jobs"`      // concurrent crawl jobs in this process
	MaxRunningPerOwner int           `yaml:"max_running_per_owner"` // fairness cap across all workers (0 = unlimited)
	LeaseTTL           time.Duration `yaml:"lease_ttl"`             // a running job whose lease expires is requeued
	PollInterval       time.Duration `yaml:"poll_interval"`
	Browser            *bool         `yaml:"browser,omitempty"` // headless browser on this worker; empty = detect at start
}

// ClusterConfig controls how other machines join this engine as workers
// ("mole join") and, on a joined node, where it syncs its configuration from.
type ClusterConfig struct {
	// On the control node.
	AdvertiseURL      string `yaml:"advertise_url"`       // URL nodes use to reach this server (shown in join commands)
	NodeDatabaseHost  string `yaml:"node_database_host"`  // MySQL host as seen from nodes (default database.host)
	NodeDatabasePort  string `yaml:"node_database_port"`  // MySQL port as seen from nodes (default database.port)
	NodeS3Endpoint    string `yaml:"node_s3_endpoint"`    // S3 endpoint as seen from nodes (default storage.s3.endpoint)
	AllowLocalStorage bool   `yaml:"allow_local_storage"` // let nodes join although storage.driver is local

	// On a joined node (written by "mole join").
	ControlURL string       `yaml:"control_url,omitempty"` // control node this worker syncs its configuration from
	JoinToken  string       `yaml:"join_token,omitempty"`
	Node       NodeSettings `yaml:"node,omitempty"`
}

// NodeSettings are a joined node's own values. They survive every config sync;
// everything else comes from the control node.
type NodeSettings struct {
	ID              string `yaml:"id,omitempty"`
	Region          string `yaml:"region,omitempty"`
	MaxRunningJobs  int    `yaml:"max_running_jobs,omitempty"`
	BrowserPoolSize int    `yaml:"browser_pool_size,omitempty"`
	Browser         *bool  `yaml:"browser,omitempty"`
}

// Joined reports whether this is a node that syncs its config from a control node.
func (c ClusterConfig) Joined() bool {
	return c.ControlURL != "" && c.JoinToken != ""
}

// ApplyNode copies the node's own values over the configuration.
func (c *Config) ApplyNode() {
	n := c.Cluster.Node
	if n.ID != "" {
		c.Worker.ID = n.ID
	}
	if n.Region != "" {
		c.Worker.Region = n.Region
	}
	if n.MaxRunningJobs > 0 {
		c.Worker.MaxRunningJobs = n.MaxRunningJobs
	}
	if n.BrowserPoolSize > 0 {
		c.Headless.PoolSize = n.BrowserPoolSize
	}
	if n.Browser != nil {
		b := *n.Browser
		c.Worker.Browser = &b
	}
}

// SecurityConfig protects the engine and the networks around it.
type SecurityConfig struct {
	AllowPrivateNetworks bool     `yaml:"allow_private_networks"` // allow crawling loopback/RFC1918/link-local targets
	AllowedCIDRs         []string `yaml:"allowed_cidrs"`          // private ranges that stay reachable when blocked
	MaxBodyBytes         int64    `yaml:"max_body_bytes"`         // response bodies are truncated beyond this size
}

// ProxyRoute sends hosts matching Match to a proxy pool and tags them with a region.
type ProxyRoute struct {
	Match  string `yaml:"match" json:"match"`
	Pool   string `yaml:"pool" json:"pool"`
	Region string `yaml:"region" json:"region"`
}

// NetworkConfig holds outbound networking defaults.
type NetworkConfig struct {
	TLSFingerprint   string              `yaml:"tls_fingerprint"`    // chrome, firefox, safari, edge, ios, go
	HostRateLimit    float64             `yaml:"host_rate_limit"`    // max requests/sec per host across all jobs (0 = off)
	ProxyPools       map[string][]string `yaml:"proxy_pools"`        // name -> proxy URLs (http, https, socks5)
	ProxyRoutes      []ProxyRoute        `yaml:"proxy_routes"`       // first match wins
	DefaultProxyPool string              `yaml:"default_proxy_pool"` // pool for hosts no route matches ("" = direct)
}

// HeadlessConfig controls the pool of long-lived headless browser processes.
type HeadlessConfig struct {
	PoolSize int `yaml:"pool_size"` // browser processes (each renders one page at a time)
}

// S3Config configures an S3-compatible object store (MinIO, ArvanCloud, AWS...).
type S3Config struct {
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	UseSSL    bool   `yaml:"use_ssl"`
	Prefix    string `yaml:"prefix"`
}

// StorageConfig selects where screenshots and offloaded page bodies are stored.
type StorageConfig struct {
	Driver        string   `yaml:"driver"`         // "local" or "s3"
	LocalDir      string   `yaml:"local_dir"`      // for the local driver
	S3            S3Config `yaml:"s3"`             // for the s3 driver
	OffloadBodies bool     `yaml:"offload_bodies"` // store saved page bodies in the object store instead of MySQL
}

// LLMConfig points at an OpenAI-compatible chat completions endpoint used for
// LLM extraction (self-hosted models or a local gateway work too).
type LLMConfig struct {
	BaseURL       string        `yaml:"base_url"` // e.g. http://127.0.0.1:3001/v1
	APIKey        string        `yaml:"api_key"`
	Model         string        `yaml:"model"`
	Timeout       time.Duration `yaml:"timeout"`
	MaxInputChars int           `yaml:"max_input_chars"`
}

// WebhooksConfig controls outgoing webhook delivery.
type WebhooksConfig struct {
	Timeout      time.Duration `yaml:"timeout"`
	MaxAttempts  int           `yaml:"max_attempts"`
	AllowPrivate bool          `yaml:"allow_private"` // allow webhooks to private addresses (e.g. a control plane on the LAN)
}

// PluginsConfig controls the Python after-crawl / after-job plugins.
type PluginsConfig struct {
	AllowInAPI    bool          `yaml:"allow_in_api"` // let /v1 clients select plugins (they run arbitrary code)
	AfterJobDelay time.Duration `yaml:"after_job_delay"`
	Timeout       time.Duration `yaml:"timeout"` // per plugin invocation
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
}

// CrawlerConfig holds crawler behavior settings
type CrawlerConfig struct {
	MaxConcurrentRequests int           `yaml:"max_concurrent_requests"`
	RequestTimeout        time.Duration `yaml:"request_timeout"`
	PolitenessDelay       time.Duration `yaml:"politeness_delay"`
	TeleportProbability   float64       `yaml:"teleport_probability"`
	MaxDepth              int           `yaml:"max_depth"`
	MaxPages              int           `yaml:"max_pages"` // 0 = unlimited
	UserAgent             string        `yaml:"user_agent"`
	MaxRetries            int           `yaml:"max_retries"`
	RespectRobotsTxt      bool          `yaml:"respect_robots_txt"`
	SkipContentDuplicates bool          `yaml:"skip_content_duplicates"` // Skip pages with same body hash
	SkipExtensions        []string      `yaml:"skip_extensions"`
	UseHeadlessBrowser    bool          `yaml:"use_headless_browser"`   // Use headless browser for JS-rendered pages
	HeadlessScriptPath    string        `yaml:"headless_script_path"`   // Path to headless fetch script (auto-detected if empty)
	HeadlessWaitSelector  string        `yaml:"headless_wait_selector"` // CSS selector to wait for before capturing content
	HeadlessRenderWait    int           `yaml:"headless_render_wait"`   // Extra seconds to wait after network idle for SPA rendering (default 5)
	EnableSemanticSearch  bool          `yaml:"enable_semantic_search"` // Enable vector-based semantic search
	EnableWordExtraction  bool          `yaml:"enable_word_extraction"` // Extract words and build inverted index during crawl
	SaveTextContent       bool          `yaml:"save_text_content"`      // Save extracted text content of crawled pages
	AfterCrawlPlugin      string        `yaml:"after_crawl_plugin"`     // Plugin id under plugins/ to run after each crawled page (empty = disabled)
	AfterJobPlugin        string        `yaml:"after_job_plugin"`       // Plugin id under plugins/ to run ~2 minutes after the job ends (empty = disabled)
	EmbeddingScriptPath   string        `yaml:"embedding_script_path"`  // Path to embedding Python script (auto-detected if empty)
	EmbeddingModel        string        `yaml:"embedding_model"`        // Sentence-transformer model name
	PythonPath            string        `yaml:"python_path"`            // Path to python3 binary (auto-detects venv if empty)
	DefaultLanguage       string        `yaml:"default_language"`       // Default language for stemming: "fa" or "en" (default "fa")
	EnableStemming        bool          `yaml:"enable_stemming"`        // Stem/lemmatise words during indexing and search
	EnableLemmatization   bool          `yaml:"enable_lemmatization"`   // Use lemmatization (more accurate) vs pure stemming
	UseCrawlPhrasesOnly   bool          `yaml:"use_crawl_phrases_only"` // true = match only extracted words from this crawl; false = also match manual search phrases
}

// ServerConfig holds web server settings
type ServerConfig struct {
	Host string `yaml:"host"`
	Port string `yaml:"port"`
}

// PortScanConfig holds TCP port-scan settings.
type PortScanConfig struct {
	Concurrent    int           `yaml:"concurrent"`
	Timeout       time.Duration `yaml:"timeout"`
	BannerTimeout time.Duration `yaml:"banner_timeout"`
}

// SubdomainConfig holds subdomain discovery settings
type SubdomainConfig struct {
	DNSServers        []string      `yaml:"dns_servers"`
	ConcurrentLookups int           `yaml:"concurrent_lookups"`
	Timeout           time.Duration `yaml:"timeout"`
	CommonSubdomains  []string      `yaml:"common_subdomains"`
}

// LoadConfig loads configuration from a YAML file, falling back to defaults if not found
func LoadConfig(configPath string) *Config {
	// Start with default configuration
	cfg := DefaultConfig()

	// Try to read the config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Config] Config file %s not found, using defaults", configPath)
		} else {
			log.Printf("[Config] Error reading config file %s: %v, using defaults", configPath, err)
		}
		cfg.applyEnvOverrides()
		return cfg
	}

	// Parse YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Printf("[Config] Error parsing config file %s: %v, using defaults", configPath, err)
		return DefaultConfig()
	}

	// A joined node's own values, then environment variable overrides
	cfg.ApplyNode()
	cfg.applyEnvOverrides()

	// Ensure common subdomains are populated if not in config
	if len(cfg.Subdomain.CommonSubdomains) == 0 {
		cfg.Subdomain.CommonSubdomains = getCommonSubdomains()
	}

	// Ensure skip extensions are populated if not in config
	if len(cfg.Crawler.SkipExtensions) == 0 {
		cfg.Crawler.SkipExtensions = getDefaultSkipExtensions()
	}

	log.Printf("[Config] Loaded configuration from %s", configPath)
	applyLegacyScriptFlags(cfg, data)
	return cfg
}

// applyLegacyScriptFlags maps the old after_*_script booleans onto plugin ids.
func applyLegacyScriptFlags(cfg *Config, data []byte) {
	var raw struct {
		Crawler struct {
			AfterCrawlScript bool `yaml:"after_crawl_script"`
			AfterJobScript   bool `yaml:"after_job_script"`
		} `yaml:"crawler"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	const legacyID = "divar"
	if cfg.Crawler.AfterCrawlPlugin == "" && raw.Crawler.AfterCrawlScript {
		cfg.Crawler.AfterCrawlPlugin = legacyID
	}
	if cfg.Crawler.AfterJobPlugin == "" && raw.Crawler.AfterJobScript {
		cfg.Crawler.AfterJobPlugin = legacyID
	}
}

// applyEnvOverrides applies environment variable overrides to the config
func (c *Config) applyEnvOverrides() {
	// Database overrides
	if v := os.Getenv("DB_HOST"); v != "" {
		c.Database.Host = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		c.Database.Port = v
	}
	if v := os.Getenv("DB_USER"); v != "" {
		c.Database.User = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		c.Database.Password = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		c.Database.DBName = v
	}

	// Server overrides
	if v := os.Getenv("SERVER_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		c.Server.Port = v
	}

	if v := os.Getenv("MOLE_API_TOKENS"); v != "" {
		c.API.Tokens = splitList(v)
	}
	if v := os.Getenv("MOLE_UI_USERNAME"); v != "" {
		c.UI.Username = v
	}
	if v := os.Getenv("MOLE_UI_PASSWORD"); v != "" {
		c.UI.Password = v
	}
	if v := os.Getenv("MOLE_WORKER_ID"); v != "" {
		c.Worker.ID = v
	}
	if v := os.Getenv("MOLE_WORKER_REGION"); v != "" {
		c.Worker.Region = v
	}
	if v := os.Getenv("MOLE_MAX_RUNNING_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Worker.MaxRunningJobs = n
		}
	}
	if v := os.Getenv("MOLE_ALLOW_PRIVATE_NETWORKS"); v != "" {
		c.Security.AllowPrivateNetworks = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("MOLE_LLM_BASE_URL"); v != "" {
		c.LLM.BaseURL = v
	}
	if v := os.Getenv("MOLE_LLM_API_KEY"); v != "" {
		c.LLM.APIKey = v
	}
	if v := os.Getenv("MOLE_LLM_MODEL"); v != "" {
		c.LLM.Model = v
	}
	if v := os.Getenv("MOLE_S3_ACCESS_KEY"); v != "" {
		c.Storage.S3.AccessKey = v
	}
	if v := os.Getenv("MOLE_S3_SECRET_KEY"); v != "" {
		c.Storage.S3.SecretKey = v
	}
	if v := os.Getenv("MOLE_WORKER_BROWSER"); v != "" {
		b := v == "1" || strings.EqualFold(v, "true")
		c.Worker.Browser = &b
	}
	if v := os.Getenv("MOLE_CONTROL_URL"); v != "" {
		c.Cluster.ControlURL = v
	}
	if v := os.Getenv("MOLE_JOIN_TOKEN"); v != "" {
		c.Cluster.JoinToken = v
	}
	if v := os.Getenv("MOLE_SEARCH_API_KEY"); v != "" {
		c.Search.APIKey = v
	}
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate reports configuration errors that would make the engine misbehave.
func (c *Config) Validate() error {
	if c.Worker.MaxRunningJobs < 1 {
		return fmt.Errorf("worker.max_running_jobs must be >= 1")
	}
	if c.Worker.LeaseTTL < 10*time.Second {
		return fmt.Errorf("worker.lease_ttl must be at least 10s")
	}
	if c.Headless.PoolSize < 1 {
		return fmt.Errorf("headless.pool_size must be >= 1")
	}
	switch c.Storage.Driver {
	case "local", "s3":
	default:
		return fmt.Errorf("storage.driver must be \"local\" or \"s3\"")
	}
	if c.Storage.Driver == "s3" && (c.Storage.S3.Endpoint == "" || c.Storage.S3.Bucket == "") {
		return fmt.Errorf("storage.s3.endpoint and storage.s3.bucket are required for the s3 driver")
	}
	switch c.Search.Provider {
	case "":
	case "searxng":
		if c.Search.BaseURL == "" {
			return fmt.Errorf("search.base_url is required for the searxng provider")
		}
	case "brave", "serper":
		if c.Search.APIKey == "" {
			return fmt.Errorf("search.api_key is required for the %s provider", c.Search.Provider)
		}
	default:
		return fmt.Errorf("search.provider must be empty, \"searxng\", \"brave\" or \"serper\"")
	}
	return nil
}

// WorkerIdentity returns the configured worker id or hostname-pid.
func (c *Config) WorkerIdentity() string {
	if c.Worker.ID != "" {
		return c.Worker.ID
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     "3306",
			User:     "root",
			Password: "1234",
			DBName:   "crawler_db",
		},
		Crawler: CrawlerConfig{
			MaxConcurrentRequests: 10,
			RequestTimeout:        30 * time.Second,
			PolitenessDelay:       1 * time.Second,
			TeleportProbability:   0.2,
			MaxDepth:              10,
			UserAgent:             "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			MaxRetries:            3,
			RespectRobotsTxt:      true,
			SkipContentDuplicates: true,
			SkipExtensions:        getDefaultSkipExtensions(),
			HeadlessRenderWait:    5,
			EnableWordExtraction:  false,
			EmbeddingModel:        "intfloat/multilingual-e5-large",
			DefaultLanguage:       "fa",
			EnableStemming:        true,
			EnableLemmatization:   true,
			UseCrawlPhrasesOnly:   true,
		},
		Server: ServerConfig{
			Port: "5050",
			Host: "0.0.0.0",
		},
		Subdomain: SubdomainConfig{
			DNSServers:        []string{"8.8.8.8:53", "8.8.4.4:53", "1.1.1.1:53"},
			ConcurrentLookups: 50,
			Timeout:           5 * time.Second,
			CommonSubdomains:  getCommonSubdomains(),
		},
		PortScan: PortScanConfig{
			Concurrent:    64,
			Timeout:       time.Second,
			BannerTimeout: 700 * time.Millisecond,
		},
		UI: UIConfig{Enabled: true},
		Worker: WorkerConfig{
			MaxRunningJobs:     4,
			MaxRunningPerOwner: 2,
			LeaseTTL:           60 * time.Second,
			PollInterval:       2 * time.Second,
		},
		Security: SecurityConfig{
			MaxBodyBytes: 10 * 1024 * 1024,
		},
		Network: NetworkConfig{
			TLSFingerprint: "chrome",
			HostRateLimit:  2,
		},
		Headless: HeadlessConfig{PoolSize: 2},
		Storage: StorageConfig{
			Driver:   "local",
			LocalDir: "data/objects",
		},
		LLM: LLMConfig{
			Timeout:       90 * time.Second,
			MaxInputChars: 24000,
		},
		Webhooks: WebhooksConfig{
			Timeout:     10 * time.Second,
			MaxAttempts: 8,
		},
		Plugins: PluginsConfig{
			AfterJobDelay: 2 * time.Minute,
			Timeout:       60 * time.Second,
		},
		Documents: DocumentsConfig{
			MaxPages: 500,
			Timeout:  60 * time.Second,
		},
		Cache: CacheConfig{
			Enabled:       true,
			TTL:           7 * 24 * time.Hour,
			MaxEntryBytes: 8 << 20,
		},
		Search: SearchConfig{
			Timeout: 15 * time.Second,
		},
	}
}

func getDefaultSkipExtensions() []string {
	return []string{
		// Images
		".jpg", ".jpeg", ".png", ".gif", ".bmp", ".ico", ".svg", ".webp", ".tiff", ".tif",
		// Documents
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods", ".odp",
		// Archives
		".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz",
		// Media
		".mp3", ".mp4", ".avi", ".mov", ".wmv", ".flv", ".mkv", ".webm", ".wav", ".ogg", ".m4a",
		// Fonts
		".woff", ".woff2", ".ttf", ".eot", ".otf",
		// Other binary files
		".exe", ".msi", ".dmg", ".pkg", ".deb", ".rpm", ".apk", ".ipa",
		".iso", ".bin", ".dat",
	}
}

func getCommonSubdomains() []string {
	return []string{
		"www", "mail", "ftp", "localhost", "webmail", "smtp", "pop", "ns1", "ns2",
		"ns3", "ns4", "dns", "dns1", "dns2", "mx", "mx1", "mx2", "admin", "api",
		"app", "apps", "beta", "blog", "cdn", "cloud", "cpanel", "dashboard",
		"demo", "dev", "developer", "docs", "email", "forum", "git", "gitlab",
		"help", "home", "imap", "info", "internal", "intranet", "login", "m",
		"mobile", "mysql", "new", "news", "office", "old", "panel", "portal",
		"proxy", "remote", "secure", "server", "shop", "staging", "static",
		"store", "support", "test", "testing", "vpn", "web", "webdisk", "wiki",
		"ww", "ww1", "ww2", "www1", "www2", "www3", "autodiscover", "autoconfig",
		"img", "images", "video", "assets", "media", "files", "download", "downloads",
		"backup", "backups", "db", "database", "sql", "cache", "search", "mail2",
		"mail3", "owa", "exchange", "sip", "lyncdiscover", "status", "monitor",
		"monitoring", "analytics", "stats", "tracking", "crm", "erp", "hr",
		"cms", "content", "auth", "sso", "oauth", "accounts", "account", "billing",
		"payment", "payments", "checkout", "cart", "order", "orders", "inventory",
		"qa", "uat", "stage", "prod", "production", "live", "preview",
	}
}
