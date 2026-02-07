package modules

import (
	"log"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// SimplePhraseDetector detects phrases in content using string matching
type SimplePhraseDetector struct {
	phrases      map[string]*regexp.Regexp
	mu           sync.RWMutex
	contextChars int // characters to include before/after match for context
}

// NewSimplePhraseDetector creates a new phrase detector
func NewSimplePhraseDetector() *SimplePhraseDetector {
	return &SimplePhraseDetector{
		phrases:      make(map[string]*regexp.Regexp),
		contextChars: 100,
	}
}

// Name returns the module name
func (p *SimplePhraseDetector) Name() string {
	return "phrase_detector"
}

// Initialize sets up the module
func (p *SimplePhraseDetector) Initialize() error {
	log.Printf("[%s] Initialized", p.Name())
	return nil
}

// Shutdown gracefully stops the module
func (p *SimplePhraseDetector) Shutdown() error {
	return nil
}

// AddPhrase adds a phrase to search for
func (p *SimplePhraseDetector) AddPhrase(phrase string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Create case-insensitive regex for the phrase
	escapedPhrase := regexp.QuoteMeta(phrase)
	re, err := regexp.Compile("(?i)" + escapedPhrase)
	if err != nil {
		log.Printf("[%s] Failed to compile phrase regex: %s", p.Name(), err)
		return
	}

	p.phrases[phrase] = re
	log.Printf("[%s] Added phrase: %s", p.Name(), phrase)
}

// AddPhrases adds multiple phrases
func (p *SimplePhraseDetector) AddPhrases(phrases []string) {
	for _, phrase := range phrases {
		p.AddPhrase(phrase)
	}
}

// RemovePhrase removes a phrase from search
func (p *SimplePhraseDetector) RemovePhrase(phrase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.phrases, phrase)
}

// DetectPhrases detects phrases in content and returns matches
func (p *SimplePhraseDetector) DetectPhrases(content string) []PhraseMatchResult {
	return p.detectInText(content, "content")
}

// DetectPhrasesInURL detects phrases in a URL
func (p *SimplePhraseDetector) DetectPhrasesInURL(rawURL string) []PhraseMatchResult {
	return p.detectInText(rawURL, "url")
}

// DetectPhrasesInAnchor detects phrases in anchor text
func (p *SimplePhraseDetector) DetectPhrasesInAnchor(anchorText string) []PhraseMatchResult {
	if anchorText == "" {
		return nil
	}
	return p.detectInText(anchorText, "anchor")
}

// detectInText performs phrase detection on text with the given match type
func (p *SimplePhraseDetector) detectInText(text string, matchType string) []PhraseMatchResult {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var results []PhraseMatchResult

	// Normalize content for searching
	normalizedContent := normalizeContent(text)

	for phrase, re := range p.phrases {
		matches := re.FindAllStringIndex(normalizedContent, -1)
		if len(matches) > 0 {
			// Get context for the first match
			firstMatch := matches[0]
			context := p.extractContext(normalizedContent, firstMatch[0], firstMatch[1])

			results = append(results, PhraseMatchResult{
				Phrase:      phrase,
				Occurrences: len(matches),
				Context:     context,
				MatchType:   matchType,
			})
		}
	}

	return results
}

// extractContext extracts surrounding text for context
func (p *SimplePhraseDetector) extractContext(content string, start, end int) string {
	// Calculate start position for context
	contextStart := start - p.contextChars
	if contextStart < 0 {
		contextStart = 0
	}

	// Calculate end position for context
	contextEnd := end + p.contextChars
	if contextEnd > len(content) {
		contextEnd = len(content)
	}

	// Ensure we don't cut UTF-8 characters in half
	for contextStart > 0 && !utf8.RuneStart(content[contextStart]) {
		contextStart--
	}
	for contextEnd < len(content) && !utf8.RuneStart(content[contextEnd]) {
		contextEnd++
	}

	context := content[contextStart:contextEnd]

	// Add ellipsis if truncated
	if contextStart > 0 {
		context = "..." + context
	}
	if contextEnd < len(content) {
		context = context + "..."
	}

	return strings.TrimSpace(context)
}

// GetPhrases returns all registered phrases
func (p *SimplePhraseDetector) GetPhrases() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	phrases := make([]string, 0, len(p.phrases))
	for phrase := range p.phrases {
		phrases = append(phrases, phrase)
	}
	return phrases
}

// normalizeContent prepares content for phrase detection
func normalizeContent(content string) string {
	// Remove excessive whitespace but preserve structure
	space := regexp.MustCompile(`[\s]+`)
	return space.ReplaceAllString(content, " ")
}
