package config

import (
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the crawler
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	Crawler   CrawlerConfig   `yaml:"crawler"`
	Server    ServerConfig    `yaml:"server"`
	Subdomain SubdomainConfig `yaml:"subdomain"`
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
	UserAgent             string        `yaml:"user_agent"`
	MaxRetries            int           `yaml:"max_retries"`
	SkipExtensions        []string      `yaml:"skip_extensions"`
}

// ServerConfig holds web server settings
type ServerConfig struct {
	Host string `yaml:"host"`
	Port string `yaml:"port"`
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
		return cfg
	}

	// Parse YAML
	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Printf("[Config] Error parsing config file %s: %v, using defaults", configPath, err)
		return DefaultConfig()
	}

	// Apply environment variable overrides
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
	return cfg
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
			UserAgent:             "Mole/1.0 (+https://mole.local)",
			MaxRetries:            3,
			SkipExtensions:        getDefaultSkipExtensions(),
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
