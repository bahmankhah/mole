package modules

import (
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxDeadlockRetries is the number of times to retry a batch insert that
// fails due to a MySQL deadlock (error 1213).
const maxDeadlockRetries = 3

// WordExtractResult holds statistics from a word extraction run.
type WordExtractResult struct {
	NewPhrases    int // new search_phrases rows inserted
	MatchesStored int // phrase_match rows created
}

// WordExtractor extracts individual words from page text content,
// normalises them (lowercase, strip punctuation, remove stop words),
// optionally stems/lemmatises via the Stemmer module, generates
// bigram/trigram n-grams, upserts them into search_phrases, and creates
// phrase_match entries that form an inverted (postings-list) index.
type WordExtractor struct {
	db         *gorm.DB
	stemmer    *Stemmer
	stopWords  map[string]struct{}
	minWordLen int
	nonAlpha   *regexp.Regexp
}

// NewWordExtractor creates a new word extractor backed by the given DB.
// The stemmer may be nil (stemming disabled).
func NewWordExtractor(db *gorm.DB, stemmer *Stemmer) *WordExtractor {
	return &WordExtractor{
		db:         db,
		stemmer:    stemmer,
		stopWords:  buildStopWords(),
		minWordLen: 2,
		nonAlpha:   regexp.MustCompile(`[^\p{L}\p{N}]+`), // keep only letters and digits (Unicode-aware)
	}
}

// Name returns the module name.
func (w *WordExtractor) Name() string { return "word_extractor" }

// Initialize sets up the module.
func (w *WordExtractor) Initialize() error {
	log.Printf("[%s] Initialized (stop words: %d, min word length: %d)", w.Name(), len(w.stopWords), w.minWordLen)
	return nil
}

// Shutdown gracefully stops the module.
func (w *WordExtractor) Shutdown() error { return nil }

// ExtractAndStore extracts words from text, upserts them into search_phrases,
// and creates PhraseMatch records.
//
// skipPhrases is the set of (lowercased) phrases already managed by the
// phrase detector – the word extractor will not create duplicate matches for
// those.
func (w *WordExtractor) ExtractAndStore(
	text string,
	crawlJobID string,
	pageID uint,
	pageURL string,
	skipPhrases map[string]uint,
) (*WordExtractResult, error) {
	result := &WordExtractResult{}

	if text == "" || crawlJobID == "" {
		return result, nil
	}

	wordCounts := w.countWords(text)
	if len(wordCounts) == 0 {
		return result, nil
	}

	// Remove words that the phrase detector already handles.
	for word := range wordCounts {
		if _, skip := skipPhrases[word]; skip {
			delete(wordCounts, word)
		}
	}
	if len(wordCounts) == 0 {
		return result, nil
	}

	// Collect distinct words.
	wordList := make([]string, 0, len(wordCounts))
	for word := range wordCounts {
		wordList = append(wordList, word)
	}

	// ── 1. Upsert search_phrases ─────────────────────────────────────
	// Bulk lookup existing phrases to minimise INSERTs.
	existingMap := make(map[string]uint, len(wordList))

	// Process in chunks of 500 (MySQL placeholder limit safeguard).
	for i := 0; i < len(wordList); i += 500 {
		end := i + 500
		if end > len(wordList) {
			end = len(wordList)
		}
		chunk := wordList[i:end]

		var existing []models.SearchPhrase
		w.db.Select("id, phrase").Where("phrase IN ?", chunk).Find(&existing)
		for _, p := range existing {
			existingMap[p.Phrase] = p.ID
		}
	}

	// Build slice of genuinely new phrases.
	var newPhrases []models.SearchPhrase
	for _, word := range wordList {
		if _, exists := existingMap[word]; !exists {
			cjID := crawlJobID // local copy for pointer
			newPhrases = append(newPhrases, models.SearchPhrase{
				Phrase:     word,
				IsActive:   true,
				CrawlJobID: &cjID,
			})
		}
	}

	if len(newPhrases) > 0 {
		// Batch insert with ignore-on-conflict (race-safe).
		// Omit associations so GORM doesn't try to upsert the CrawlJob relation.
		if err := w.db.Omit(clause.Associations).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "phrase"}},
			DoNothing: true,
		}).CreateInBatches(&newPhrases, 200).Error; err != nil {
			log.Printf("[WordExtractor] batch insert phrases: %v", err)
		}

		// Re-fetch IDs for the words we just tried to insert.
		insertedWords := make([]string, 0, len(newPhrases))
		for _, p := range newPhrases {
			insertedWords = append(insertedWords, p.Phrase)
		}
		for i := 0; i < len(insertedWords); i += 500 {
			end := i + 500
			if end > len(insertedWords) {
				end = len(insertedWords)
			}
			var fetched []models.SearchPhrase
			w.db.Select("id, phrase").Where("phrase IN ?", insertedWords[i:end]).Find(&fetched)
			for _, p := range fetched {
				existingMap[p.Phrase] = p.ID
			}
		}
		result.NewPhrases = len(newPhrases)
	}

	// ── 2. Create PhraseMatch records (inverted-index entries) ───────
	lowerText := strings.ToLower(text)
	now := time.Now()

	matches := make([]models.PhraseMatch, 0, len(wordCounts))
	skipped := 0
	for word, count := range wordCounts {
		phraseID, ok := existingMap[word]
		if !ok {
			skipped++
			continue
		}

		ctx := extractWordContext(lowerText, word, 60)

		pid := phraseID // local copy for pointer
		matches = append(matches, models.PhraseMatch{
			CrawlJobID:     crawlJobID,
			PageID:         pageID,
			SearchPhraseID: &pid,
			URL:            pageURL,
			Phrase:         word,
			MatchType:      models.MatchTypeContent,
			Context:        ctx,
			Occurrences:    count,
			FoundAt:        now,
		})
	}

	log.Printf("[WordExtractor] page_id=%d url=%s: %d unique words, existingMap=%d, skipped=%d, matches=%d",
		pageID, pageURL, len(wordCounts)+skipped, len(existingMap), skipped, len(matches))

	if len(matches) > 0 {
		stored, err := w.batchCreateMatches(matches, pageID)
		if err != nil {
			return result, err
		}
		result.MatchesStored = stored
	}

	return result, nil
}

// batchCreateMatches inserts PhraseMatch records in batches with:
//   - clause.Associations omitted (prevents GORM from touching FK associations)
//   - deadlock retry (concurrent workers may deadlock on FK gap-locks)
//   - per-record fallback when a batch fails for non-deadlock reasons
func (w *WordExtractor) batchCreateMatches(matches []models.PhraseMatch, pageID uint) (int, error) {
	const batchSize = 100
	stored := 0

	for i := 0; i < len(matches); i += batchSize {
		end := i + batchSize
		if end > len(matches) {
			end = len(matches)
		}
		batch := matches[i:end]

		var batchErr error
		for attempt := 1; attempt <= maxDeadlockRetries; attempt++ {
			batchErr = w.db.Session(&gorm.Session{FullSaveAssociations: false}).
				Omit(clause.Associations).
				CreateInBatches(&batch, batchSize).Error
			if batchErr == nil {
				stored += len(batch)
				break
			}
			if isDeadlock(batchErr) {
				log.Printf("[WordExtractor] deadlock on batch page_id=%d (attempt %d/%d), retrying…",
					pageID, attempt, maxDeadlockRetries)
				time.Sleep(time.Duration(attempt*50) * time.Millisecond)
				continue
			}
			break // non-deadlock error — stop retrying
		}

		if batchErr != nil {
			// Batch failed — fall back to one-by-one insert so a single bad
			// record doesn't discard the whole batch.
			log.Printf("[WordExtractor] batch insert failed (page_id=%d, batch %d-%d): %v — falling back to per-record insert",
				pageID, i, end, batchErr)
			for j := range batch {
				if err := w.db.Omit(clause.Associations).Create(&batch[j]).Error; err != nil {
					log.Printf("[WordExtractor] per-record insert fail page_id=%d phrase=%q: %v",
						pageID, batch[j].Phrase, err)
				} else {
					stored++
				}
			}
		}
	}

	if stored > 0 {
		log.Printf("[WordExtractor] page_id=%d: stored %d/%d phrase matches", pageID, stored, len(matches))
	}
	return stored, nil
}

// isDeadlock returns true if err is a MySQL deadlock (error 1213).
func isDeadlock(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadlock") || strings.Contains(msg, "1213")
}

// ──────────────────────────────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────────────────────────────

// countWords tokenises, normalises, optionally stems, and counts unique
// words plus bigram/trigram n-grams in text.
func (w *WordExtractor) countWords(text string) map[string]int {
	text = strings.ToLower(text)
	text = w.nonAlpha.ReplaceAllString(text, " ")
	fields := strings.Fields(text)

	// Filter tokens: remove short, stop, and numeric tokens.
	filtered := make([]string, 0, len(fields))
	for _, word := range fields {
		if len(word) < w.minWordLen {
			continue
		}
		if _, stop := w.stopWords[word]; stop {
			continue
		}
		if isNumericToken(word) {
			continue
		}
		filtered = append(filtered, word)
	}

	// Stem the filtered tokens if a stemmer is available.
	if w.stemmer != nil && w.stemmer.Enabled() && len(filtered) > 0 {
		filtered = w.stemmer.StemTokens(filtered)
	}

	counts := make(map[string]int, len(filtered)*2)

	// Unigrams
	for _, tok := range filtered {
		if tok == "" {
			continue
		}
		counts[tok]++
	}

	// Bigrams
	for i := 0; i+1 < len(filtered); i++ {
		if filtered[i] == "" || filtered[i+1] == "" {
			continue
		}
		bigram := filtered[i] + " " + filtered[i+1]
		counts[bigram]++
	}

	// Trigrams
	for i := 0; i+2 < len(filtered); i++ {
		if filtered[i] == "" || filtered[i+1] == "" || filtered[i+2] == "" {
			continue
		}
		trigram := filtered[i] + " " + filtered[i+1] + " " + filtered[i+2]
		counts[trigram]++
	}

	return counts
}

// NormalizeQueryTokens applies the same normalisation pipeline used during
// indexing (lowercase, strip punctuation, stop-word removal, stemming) to a
// search query string and returns the ordered list of cleaned tokens.
// This ensures query terms match the indexed forms.
func (w *WordExtractor) NormalizeQueryTokens(text string) []string {
	text = strings.ToLower(text)
	text = w.nonAlpha.ReplaceAllString(text, " ")
	fields := strings.Fields(text)

	filtered := make([]string, 0, len(fields))
	for _, word := range fields {
		if len(word) < w.minWordLen {
			continue
		}
		if _, stop := w.stopWords[word]; stop {
			continue
		}
		if isNumericToken(word) {
			continue
		}
		filtered = append(filtered, word)
	}

	if w.stemmer != nil && w.stemmer.Enabled() && len(filtered) > 0 {
		filtered = w.stemmer.StemTokens(filtered)
	}

	// Remove empty tokens that stemming may produce.
	result := make([]string, 0, len(filtered))
	for _, tok := range filtered {
		if tok != "" {
			result = append(result, tok)
		}
	}
	return result
}

// extractWordContext returns a short snippet around the first occurrence of word.
func extractWordContext(text, word string, radius int) string {
	idx := strings.Index(text, word)
	if idx < 0 {
		return ""
	}
	start := idx - radius
	if start < 0 {
		start = 0
	}
	end := idx + len(word) + radius
	if end > len(text) {
		end = len(text)
	}
	ctx := text[start:end]
	if start > 0 {
		ctx = "..." + ctx
	}
	if end < len(text) {
		ctx = ctx + "..."
	}
	return strings.TrimSpace(ctx)
}

// isNumericToken returns true if every rune in s is a digit.
func isNumericToken(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// ──────────────────────────────────────────────────────────────────────
// Stop word list
// ──────────────────────────────────────────────────────────────────────

// buildStopWords returns a set of English and Persian stop words plus common
// web noise that carry little semantic value in an information-retrieval context.
func buildStopWords() map[string]struct{} {
	words := []string{
		// ── English function words ──
		"a", "about", "above", "after", "again", "against", "all", "am", "an",
		"and", "any", "are", "aren", "arent", "as", "at",
		"be", "because", "been", "before", "being", "below", "between", "both",
		"but", "by",
		"can", "cant", "cannot", "could", "couldn", "couldnt",
		"did", "didn", "didnt", "do", "does", "doesn", "doesnt", "doing",
		"don", "dont", "down", "during",
		"each", "else", "even", "every",
		"few", "for", "from", "further",
		"get", "gets", "got",
		"had", "hadn", "hadnt", "has", "hasn", "hasnt", "have", "haven",
		"havent", "having", "he", "hed", "hell", "hes", "her", "here",
		"heres", "hers", "herself", "him", "himself", "his", "how", "hows",
		"i", "id", "ill", "im", "ive", "if", "in", "into", "is", "isn",
		"isnt", "it", "its", "itself",
		"just",
		"let", "lets",
		"ll",
		"me", "might", "more", "most", "mustn", "mustnt", "my", "myself",
		"no", "nor", "not", "now",
		"of", "off", "on", "once", "only", "or", "other", "ought", "our",
		"ours", "ourselves", "out", "over", "own",
		"re",
		"same", "shan", "shant", "she", "shed", "shell", "shes", "should",
		"shouldn", "shouldnt", "so", "some", "such",
		"than", "that", "thats", "the", "their", "theirs", "them",
		"themselves", "then", "there", "theres", "these", "they", "theyd",
		"theyll", "theyre", "theyve", "this", "those", "through", "to",
		"too",
		"under", "until", "up", "us",
		"ve", "very",
		"was", "wasn", "wasnt", "we", "wed", "well", "were", "werent",
		"weve", "what", "whats", "when", "whens", "where", "wheres",
		"which", "while", "who", "whos", "whom", "why", "whys", "will",
		"with", "won", "wont", "would", "wouldn", "wouldnt",
		"you", "youd", "youll", "youre", "youve", "your", "yours",
		"yourself", "yourselves",

		// ── Persian / Farsi stop words (حروف اضافه، ضمایر، افعال ربطی) ──
		// Pronouns & demonstratives
		"من", "تو", "او", "ما", "شما", "آنها", "ایشان", "این", "آن",
		"اینها", "آنان", "خود", "خودم", "خودت", "خودش", "خودمان",
		"خودتان", "خودشان", "همین", "همان", "چنین", "چنان",
		// Prepositions & postpositions
		"از", "با", "بر", "به", "تا", "در", "برای", "بدون", "جز",
		"درباره", "مانند", "پس", "پیش", "زیر", "روی", "میان", "نزد",
		"بالای", "پایین", "جلوی", "عقب", "کنار", "بین", "توسط",
		// Conjunctions
		"و", "یا", "اما", "ولی", "که", "اگر", "چون", "زیرا", "لیکن",
		"بلکه", "گرچه", "هرچند", "حتی", "نیز", "هم", "همچنین",
		// Auxiliary & copula verbs (present)
		"است", "هست", "نیست", "بود", "شد", "شده", "شود", "شوند",
		"باشد", "باشند", "باشم", "باشی", "باشیم", "باشید",
		"هستم", "هستی", "هستیم", "هستید", "هستند",
		"بودم", "بودی", "بودیم", "بودید", "بودند",
		// Common verb prefixes/forms
		"می", "نمی", "بی", "خواهد", "خواهند", "خواهم", "خواهی",
		"خواهیم", "خواهید",
		// Determiners / quantifiers
		"هر", "هیچ", "همه", "بعضی", "برخی", "چند", "یک", "دو", "سه",
		"هزار", "صد", "ده", "بسیار", "خیلی", "کمی", "اندکی", "تمام",
		"دیگر", "دیگری", "فقط", "تنها",
		// Adverbs / particles
		"را", "تر", "ترین", "ها", "های", "ای",
		"چه", "چرا", "کجا", "کی", "چگونه", "چطور",
		"بله", "نه", "آری", "خیر", "بعد", "قبل", "حال", "حالا",
		"اکنون", "هنوز", "دوباره", "باز", "پیش", "سپس",
		"اینجا", "آنجا", "اینگونه", "آنگونه",
		// Time-related
		"وقتی", "زمانی", "همیشه", "هرگز", "گاهی", "اغلب",
		"امروز", "دیروز", "فردا", "سال", "ماه", "روز",
		// Common low-value words
		"مورد", "حدود", "طور", "صورت", "جای", "طریق", "نوع",
		"بار", "مرتبه", "بخش", "قسمت", "شکل", "حال",

		// ── Common web / markup noise ──
		"www", "http", "https", "com", "org", "net", "html", "htm", "php",
		"asp", "aspx", "jsp", "css", "js", "nbsp", "amp", "quot", "lt", "gt",
		"div", "span", "class", "href", "src",
		// ── Very common low-value words ──
		"also", "back", "like", "may", "much", "new", "one", "said",
		"since", "still", "two", "us", "use", "way", "will",
	}

	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}
