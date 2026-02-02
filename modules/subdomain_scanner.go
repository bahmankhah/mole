package modules

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/resolver/crawler/config"
)

// SubdomainScanner implements subdomain discovery
type SubdomainScanner struct {
	config    config.SubdomainConfig
	resolver  *net.Resolver
	mu        sync.Mutex
	isRunning bool
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewSubdomainScanner creates a new subdomain scanner
func NewSubdomainScanner(cfg config.SubdomainConfig) *SubdomainScanner {
	return &SubdomainScanner{
		config: cfg,
	}
}

// Name returns the module name
func (s *SubdomainScanner) Name() string {
	return "subdomain_scanner"
}

// Initialize sets up the module
func (s *SubdomainScanner) Initialize() error {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	// Create custom resolver with configured DNS servers
	s.resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: s.config.Timeout,
			}
			// Use the first configured DNS server
			if len(s.config.DNSServers) > 0 {
				return d.DialContext(ctx, "udp", s.config.DNSServers[0])
			}
			return d.DialContext(ctx, network, address)
		},
	}

	log.Printf("[%s] Initialized with %d common subdomains to check", s.Name(), len(s.config.CommonSubdomains))
	return nil
}

// Shutdown gracefully stops the module
func (s *SubdomainScanner) Shutdown() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// DiscoverSubdomains finds subdomains for a domain using DNS enumeration
func (s *SubdomainScanner) DiscoverSubdomains(domain string, callback func(subdomain string)) error {
	s.mu.Lock()
	if s.isRunning {
		s.mu.Unlock()
		return fmt.Errorf("subdomain scan already running")
	}
	s.isRunning = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}()

	log.Printf("[%s] Starting subdomain discovery for: %s", s.Name(), domain)

	// Clean domain
	domain = cleanDomain(domain)

	// Channel for subdomains to check
	subdomainChan := make(chan string, len(s.config.CommonSubdomains))
	resultChan := make(chan subdomainResult, s.config.ConcurrentLookups)

	// Worker pool
	var wg sync.WaitGroup
	for i := 0; i < s.config.ConcurrentLookups; i++ {
		wg.Add(1)
		go s.worker(domain, subdomainChan, resultChan, &wg)
	}

	// Send subdomains to check
	go func() {
		for _, sub := range s.config.CommonSubdomains {
			select {
			case <-s.ctx.Done():
				break
			case subdomainChan <- sub:
			}
		}
		close(subdomainChan)
	}()

	// Close result channel when workers are done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	foundCount := 0
	for result := range resultChan {
		if result.exists {
			foundCount++
			fullSubdomain := fmt.Sprintf("%s.%s", result.subdomain, domain)
			log.Printf("[%s] Found subdomain: %s (IP: %s)", s.Name(), fullSubdomain, result.ip)
			callback(fullSubdomain)
		}
	}

	log.Printf("[%s] Subdomain discovery completed. Found %d subdomains", s.Name(), foundCount)
	return nil
}

type subdomainResult struct {
	subdomain string
	exists    bool
	ip        string
}

func (s *SubdomainScanner) worker(domain string, subdomains <-chan string, results chan<- subdomainResult, wg *sync.WaitGroup) {
	defer wg.Done()

	for subdomain := range subdomains {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		fullDomain := fmt.Sprintf("%s.%s", subdomain, domain)
		exists, ip := s.checkSubdomain(fullDomain)
		results <- subdomainResult{
			subdomain: subdomain,
			exists:    exists,
			ip:        ip,
		}
	}
}

func (s *SubdomainScanner) checkSubdomain(domain string) (bool, string) {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.Timeout)
	defer cancel()

	ips, err := s.resolver.LookupHost(ctx, domain)
	if err != nil {
		return false, ""
	}

	if len(ips) > 0 {
		return true, ips[0]
	}
	return false, ""
}

// cleanDomain removes protocol and path from domain
func cleanDomain(domain string) string {
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "www.")

	// Remove path if present
	if idx := strings.Index(domain, "/"); idx != -1 {
		domain = domain[:idx]
	}

	// Remove port if present
	if idx := strings.Index(domain, ":"); idx != -1 {
		domain = domain[:idx]
	}

	return strings.TrimSpace(strings.ToLower(domain))
}

// CleanDomain is the exported version of cleanDomain
func CleanDomain(domain string) string {
	return cleanDomain(domain)
}

// GetDefaultSeedURLs returns default seed URLs for a domain
func GetDefaultSeedURLs(domain string) []string {
	domain = cleanDomain(domain)

	return []string{
		fmt.Sprintf("https://%s", domain),
		fmt.Sprintf("https://%s/robots.txt", domain),
		fmt.Sprintf("https://%s/sitemap.xml", domain),
		fmt.Sprintf("https://%s/sitemap_index.xml", domain),
		fmt.Sprintf("https://www.%s", domain),
		fmt.Sprintf("https://www.%s/robots.txt", domain),
		fmt.Sprintf("https://www.%s/sitemap.xml", domain),
	}
}
