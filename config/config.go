package config

import (
	"os"
	"time"
)

// Config holds all configuration for the crawler
type Config struct {
	Database  DatabaseConfig
	Crawler   CrawlerConfig
	Server    ServerConfig
	Subdomain SubdomainConfig
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

// CrawlerConfig holds crawler behavior settings
type CrawlerConfig struct {
	MaxConcurrentRequests int
	RequestTimeout        time.Duration
	PolitenessDelay       time.Duration
	TeleportProbability   float64
	MaxDepth              int
	UserAgent             string
	MaxRetries            int
}

// ServerConfig holds web server settings
type ServerConfig struct {
	Port string
	Host string
}

// SubdomainConfig holds subdomain discovery settings
type SubdomainConfig struct {
	DNSServers        []string
	ConcurrentLookups int
	Timeout           time.Duration
	CommonSubdomains  []string
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "3306"),
			User:     getEnv("DB_USER", "root"),
			Password: getEnv("DB_PASSWORD", "1234"),
			DBName:   getEnv("DB_NAME", "crawler_db"),
		},
		Crawler: CrawlerConfig{
			MaxConcurrentRequests: 10,
			RequestTimeout:        30 * time.Second,
			PolitenessDelay:       1 * time.Second,
			TeleportProbability:   0.2,
			MaxDepth:              10,
			UserAgent:             "ResolveCrawler/1.0 (+https://resolver.local)",
			MaxRetries:            3,
		},
		Server: ServerConfig{
			Port: getEnv("SERVER_PORT", "5050"),
			Host: getEnv("SERVER_HOST", "0.0.0.0"),
		},
		Subdomain: SubdomainConfig{
			DNSServers:        []string{"8.8.8.8:53", "8.8.4.4:53", "1.1.1.1:53"},
			ConcurrentLookups: 50,
			Timeout:           5 * time.Second,
			CommonSubdomains:  getCommonSubdomains(),
		},
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
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
