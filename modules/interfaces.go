package modules

// Module defines the interface for all crawler modules
type Module interface {
	// Name returns the module name
	Name() string
	// Initialize sets up the module
	Initialize() error
	// Shutdown gracefully stops the module
	Shutdown() error
}

// URLProcessor is a module that processes URLs
type URLProcessor interface {
	Module
	// ProcessURL processes a URL and returns the cleaned/normalized version
	ProcessURL(rawURL string) (string, error)
}

// ContentProcessor is a module that processes page content
type ContentProcessor interface {
	Module
	// ProcessContent processes the content of a crawled page
	ProcessContent(url string, content []byte, contentType string) error
}

// LinkExtractor is a module that extracts links from content
type LinkExtractor interface {
	Module
	// ExtractLinks extracts all links from the content
	ExtractLinks(baseURL string, content []byte) ([]string, error)
}

// DuplicateDetector is a module that detects duplicate content or URLs
type DuplicateDetector interface {
	Module
	// IsDuplicate checks if the URL/content has been seen before
	IsDuplicate(identifier string) bool
	// MarkSeen marks an identifier as seen
	MarkSeen(identifier string)
	// Reset clears all seen identifiers
	Reset()
}

// FrontierManager manages the URL frontier queue
type FrontierManager interface {
	Module
	// AddURL adds a URL to the frontier
	AddURL(url string, depth int, parentURL string) error
	// GetNextURL retrieves the next URL to crawl
	GetNextURL() (string, int, error)
	// MarkCompleted marks a URL as completed
	MarkCompleted(url string) error
	// MarkFailed marks a URL as failed
	MarkFailed(url string, err error) error
	// Size returns the current frontier size
	Size() int
}

// SubdomainDiscoverer discovers subdomains for a domain
type SubdomainDiscoverer interface {
	Module
	// DiscoverSubdomains finds subdomains for a domain
	DiscoverSubdomains(domain string, callback func(subdomain string)) error
}

// PhraseDetector detects specific phrases in content
type PhraseDetector interface {
	Module
	// AddPhrase adds a phrase to search for
	AddPhrase(phrase string)
	// DetectPhrases detects phrases in content and returns matches
	DetectPhrases(content string) []PhraseMatchResult
}

// PhraseMatchResult represents a phrase match result
type PhraseMatchResult struct {
	Phrase      string
	Occurrences int
	Context     string
	MatchType   string // "content", "url", "anchor"
}
