package modules

import (
	"errors"
	"log"
	"math/rand"
	"sync"
	"time"
)

// RandomSurferFrontier implements the random surfer model for URL selection
// With probability (1-teleportProbability), follow a random outlink
// With probability teleportProbability, teleport to a random page in frontier
type RandomSurferFrontier struct {
	mu                  sync.Mutex
	pendingURLs         []FrontierItem
	allURLs             map[string]struct{} // All URLs ever added for teleportation
	teleportProbability float64
	rng                 *rand.Rand
	urlCleaner          *URLCleaner
	duplicateDetector   *ExactDuplicateDetector
}

// FrontierItem represents an item in the frontier queue
type FrontierItem struct {
	URL       string
	Depth     int
	ParentURL string
	Priority  int
}

// NewRandomSurferFrontier creates a new random surfer frontier
func NewRandomSurferFrontier(teleportProb float64, urlCleaner *URLCleaner, dupDetector *ExactDuplicateDetector) *RandomSurferFrontier {
	return &RandomSurferFrontier{
		pendingURLs:         make([]FrontierItem, 0),
		allURLs:             make(map[string]struct{}),
		teleportProbability: teleportProb,
		rng:                 rand.New(rand.NewSource(time.Now().UnixNano())),
		urlCleaner:          urlCleaner,
		duplicateDetector:   dupDetector,
	}
}

// Name returns the module name
func (f *RandomSurferFrontier) Name() string {
	return "random_surfer_frontier"
}

// Initialize sets up the module
func (f *RandomSurferFrontier) Initialize() error {
	log.Printf("[%s] Initialized with teleport probability: %.2f", f.Name(), f.teleportProbability)
	return nil
}

// Shutdown gracefully stops the module
func (f *RandomSurferFrontier) Shutdown() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingURLs = nil
	f.allURLs = nil
	return nil
}

// AddURL adds a URL to the frontier if not already seen
func (f *RandomSurferFrontier) AddURL(rawURL string, depth int, parentURL string) error {
	// Clean the URL
	cleanedURL, err := f.urlCleaner.ProcessURL(rawURL)
	if err != nil {
		return err
	}

	// Get URL hash
	urlHash := f.urlCleaner.HashURL(cleanedURL)

	// Check for duplicates (atomic check and mark)
	if f.duplicateDetector.IsDuplicateOrMark(urlHash) {
		return nil // Already seen, skip
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Add to pending URLs
	f.pendingURLs = append(f.pendingURLs, FrontierItem{
		URL:       cleanedURL,
		Depth:     depth,
		ParentURL: parentURL,
		Priority:  0,
	})

	// Add to all URLs for teleportation
	f.allURLs[cleanedURL] = struct{}{}

	return nil
}

// AddURLs adds multiple URLs to the frontier
func (f *RandomSurferFrontier) AddURLs(urls []string, depth int, parentURL string) int {
	added := 0
	for _, url := range urls {
		if err := f.AddURL(url, depth, parentURL); err == nil {
			added++
		}
	}
	return added
}

// GetNextURL retrieves the next URL to crawl using random surfer model
func (f *RandomSurferFrontier) GetNextURL() (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.pendingURLs) == 0 {
		return "", 0, errors.New("frontier is empty")
	}

	var selectedItem FrontierItem
	var selectedIdx int

	// Random surfer decision
	if f.rng.Float64() < f.teleportProbability && len(f.allURLs) > 0 {
		// Teleport: select randomly from ALL known URLs
		allURLsList := make([]string, 0, len(f.allURLs))
		for url := range f.allURLs {
			allURLsList = append(allURLsList, url)
		}
		randomURL := allURLsList[f.rng.Intn(len(allURLsList))]

		// Find this URL in pending, or create new item with depth 0
		found := false
		for i, item := range f.pendingURLs {
			if item.URL == randomURL {
				selectedItem = item
				selectedIdx = i
				found = true
				break
			}
		}

		if !found {
			// URL already crawled or removed, pick from pending instead
			selectedIdx = f.rng.Intn(len(f.pendingURLs))
			selectedItem = f.pendingURLs[selectedIdx]
		}
	} else {
		// Follow: select uniformly from pending URLs
		selectedIdx = f.rng.Intn(len(f.pendingURLs))
		selectedItem = f.pendingURLs[selectedIdx]
	}

	// Remove selected item from pending
	f.pendingURLs = append(f.pendingURLs[:selectedIdx], f.pendingURLs[selectedIdx+1:]...)

	return selectedItem.URL, selectedItem.Depth, nil
}

// Size returns the current frontier size (pending URLs)
func (f *RandomSurferFrontier) Size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pendingURLs)
}

// TotalKnownURLs returns total URLs known (for teleportation)
func (f *RandomSurferFrontier) TotalKnownURLs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.allURLs)
}

// MarkCompleted marks a URL as completed (no-op in memory implementation)
func (f *RandomSurferFrontier) MarkCompleted(url string) error {
	return nil
}

// MarkFailed marks a URL as failed
func (f *RandomSurferFrontier) MarkFailed(url string, err error) error {
	return nil
}

// Reset clears the frontier
func (f *RandomSurferFrontier) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingURLs = make([]FrontierItem, 0)
	f.allURLs = make(map[string]struct{})
}

// SelectNextFromLinks implements random surfer selection from current page links
// Returns the selected link or empty string if should teleport
func (f *RandomSurferFrontier) SelectNextFromLinks(links []string) (string, bool) {
	if len(links) == 0 {
		return "", true // No links, must teleport
	}

	// Random surfer decision
	if f.rng.Float64() < f.teleportProbability {
		return "", true // Teleport
	}

	// Select uniformly from available links
	return links[f.rng.Intn(len(links))], false
}
