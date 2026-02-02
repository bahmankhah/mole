package modules

import (
	"log"
	"sync"
)

// ExactDuplicateDetector detects exact URL duplicates using a hash set
type ExactDuplicateDetector struct {
	seen map[string]struct{}
	mu   sync.RWMutex
}

// NewExactDuplicateDetector creates a new exact duplicate detector
func NewExactDuplicateDetector() *ExactDuplicateDetector {
	return &ExactDuplicateDetector{
		seen: make(map[string]struct{}),
	}
}

// Name returns the module name
func (d *ExactDuplicateDetector) Name() string {
	return "exact_duplicate_detector"
}

// Initialize sets up the module
func (d *ExactDuplicateDetector) Initialize() error {
	log.Printf("[%s] Initialized", d.Name())
	return nil
}

// Shutdown gracefully stops the module
func (d *ExactDuplicateDetector) Shutdown() error {
	d.Reset()
	return nil
}

// IsDuplicate checks if the identifier (URL hash) has been seen before
func (d *ExactDuplicateDetector) IsDuplicate(identifier string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, exists := d.seen[identifier]
	return exists
}

// MarkSeen marks an identifier as seen
func (d *ExactDuplicateDetector) MarkSeen(identifier string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[identifier] = struct{}{}
}

// IsDuplicateOrMark atomically checks and marks in one operation
func (d *ExactDuplicateDetector) IsDuplicateOrMark(identifier string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.seen[identifier]; exists {
		return true
	}
	d.seen[identifier] = struct{}{}
	return false
}

// Reset clears all seen identifiers
func (d *ExactDuplicateDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = make(map[string]struct{})
}

// Size returns the number of seen identifiers
func (d *ExactDuplicateDetector) Size() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.seen)
}

// LoadFromHashes loads a set of hashes into the detector (for resuming crawls)
func (d *ExactDuplicateDetector) LoadFromHashes(hashes []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, hash := range hashes {
		d.seen[hash] = struct{}{}
	}
	log.Printf("[%s] Loaded %d hashes", d.Name(), len(hashes))
}
