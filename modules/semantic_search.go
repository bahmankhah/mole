package modules

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SemanticSearcher handles embedding generation, FAISS indexing, and semantic search.
type SemanticSearcher struct {
	db         *gorm.DB
	config     config.CrawlerConfig
	scriptPath string
	indexPath  string
	pythonCmd  string
	mu         sync.RWMutex
}

// NewSemanticSearcher creates a new semantic searcher instance.
func NewSemanticSearcher(cfg config.CrawlerConfig, db *gorm.DB) *SemanticSearcher {
	scriptPath := cfg.EmbeddingScriptPath
	if scriptPath == "" {
		// Auto-detect script path relative to working directory
		candidates := []string{
			"scripts/semantic_embed.py",
			"./scripts/semantic_embed.py",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				scriptPath = c
				break
			}
		}
		if scriptPath == "" {
			scriptPath = "scripts/semantic_embed.py"
		}
	}

	// Index stored in a data directory
	indexDir := "data/faiss"
	os.MkdirAll(indexDir, 0755)
	indexPath := filepath.Join(indexDir, "pages.index")

	pythonCmd := FindPython(cfg.PythonPath)
	log.Printf("[SemanticSearcher] Initialized: script=%s, python=%s, model=%s",
		scriptPath, pythonCmd, cfg.EmbeddingModel)

	return &SemanticSearcher{
		db:         db,
		config:     cfg,
		scriptPath: scriptPath,
		indexPath:  indexPath,
		pythonCmd:  pythonCmd,
	}
}

// embeddingRequest is sent to the Python script via stdin
type embeddingRequest struct {
	Command        string              `json:"command"`
	Texts          []string            `json:"texts,omitempty"`
	Model          string              `json:"model,omitempty"`
	Query          string              `json:"query,omitempty"`
	IndexPath      string              `json:"index_path,omitempty"`
	TopK           int                 `json:"top_k,omitempty"`
	EmbeddingsData []embeddingDataItem `json:"embeddings_data,omitempty"`
}

type embeddingDataItem struct {
	ID     uint      `json:"id"`
	Vector []float64 `json:"vector"`
}

// embeddingResponse is returned from the Python script
type embeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
	Dimension  int         `json:"dimension"`
	Error      string      `json:"error"`
}

type indexResponse struct {
	TotalIndexed int    `json:"total_indexed"`
	Error        string `json:"error"`
}

type searchResponse struct {
	Results []searchHit `json:"results"`
	Error   string      `json:"error"`
}

type searchHit struct {
	ID    int     `json:"id"`
	Score float64 `json:"score"`
}

// callPython runs the Python script with the given request and decodes the response.
func (s *SemanticSearcher) callPython(ctx context.Context, req *embeddingRequest) ([]byte, error) {
	input, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, s.pythonCmd, s.scriptPath)
	cmd.Stdin = strings.NewReader(string(input))

	// Inherit environment but strip proxy vars that can break model downloads.
	// The ALL_PROXY=socks:// scheme is not supported by the requests library
	// used internally by sentence-transformers / huggingface_hub.
	filteredEnv := make([]string, 0, len(os.Environ()))
	for _, env := range os.Environ() {
		upper := strings.ToUpper(env)
		if strings.HasPrefix(upper, "HTTP_PROXY=") ||
			strings.HasPrefix(upper, "HTTPS_PROXY=") ||
			strings.HasPrefix(upper, "ALL_PROXY=") ||
			strings.HasPrefix(upper, "FTP_PROXY=") {
			continue
		}
		filteredEnv = append(filteredEnv, env)
	}
	cmd.Env = filteredEnv

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("python script failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("run python: %w", err)
	}
	return output, nil
}

// EmbedTexts generates embeddings for a batch of texts.
func (s *SemanticSearcher) EmbedTexts(ctx context.Context, texts []string) ([][]float64, int, error) {
	model := s.config.EmbeddingModel
	if model == "" {
		model = "intfloat/multilingual-e5-large"
	}

	req := &embeddingRequest{
		Command: "embed",
		Texts:   texts,
		Model:   model,
	}

	output, err := s.callPython(ctx, req)
	if err != nil {
		return nil, 0, err
	}

	var resp embeddingResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, 0, fmt.Errorf("decode embed response: %w", err)
	}
	if resp.Error != "" {
		return nil, 0, fmt.Errorf("embed error: %s", resp.Error)
	}

	return resp.Embeddings, resp.Dimension, nil
}

// EmbedAndStore generates an embedding for a crawled page and stores it in the database.
// It skips pages that already have an embedding with the same text hash.
func (s *SemanticSearcher) EmbedAndStore(ctx context.Context, page *models.CrawledPage, textContent string) error {
	if textContent == "" || page == nil {
		return nil
	}

	// Truncate to ~1000 words to keep embedding meaningful and fast
	words := strings.Fields(textContent)
	if len(words) > 1000 {
		words = words[:1000]
	}
	text := page.Title + ". " + strings.Join(words, " ")

	// E5 models require "passage: " prefix for documents
	if isE5Model(s.config.EmbeddingModel) {
		text = "passage: " + text
	}

	// Compute hash to avoid re-embedding identical content
	h := sha256.Sum256([]byte(text))
	textHash := hex.EncodeToString(h[:])

	// Check if an identical embedding already exists
	var existing models.PageEmbedding
	if err := s.db.Where("page_id = ? AND text_hash = ?", page.ID, textHash).First(&existing).Error; err == nil {
		return nil // already embedded with same content
	}

	// Generate embedding
	embeddings, _, err := s.EmbedTexts(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed page %d: %w", page.ID, err)
	}
	if len(embeddings) == 0 {
		return fmt.Errorf("no embedding returned for page %d", page.ID)
	}

	// Serialize float64 slice to bytes (as float32 to save space)
	embBytes := float64SliceToBytes(embeddings[0])

	emb := &models.PageEmbedding{
		CrawlJobID: page.CrawlJobID,
		PageID:     page.ID,
		URL:        page.URL,
		Title:      page.Title,
		Embedding:  embBytes,
		TextHash:   textHash,
	}

	// Upsert: update embedding if page_id already exists
	result := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "page_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"embedding", "text_hash", "title", "url", "crawl_job_id"}),
	}).Create(emb)

	return result.Error
}

// RebuildIndex reads all embeddings from the database and builds a FAISS index.
func (s *SemanticSearcher) RebuildIndex(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Println("[SemanticSearch] Rebuilding FAISS index...")

	var embeddings []models.PageEmbedding
	if err := s.db.Find(&embeddings).Error; err != nil {
		return fmt.Errorf("load embeddings: %w", err)
	}

	if len(embeddings) == 0 {
		log.Println("[SemanticSearch] No embeddings to index")
		return nil
	}

	// Convert to format expected by the Python script
	data := make([]embeddingDataItem, 0, len(embeddings))
	for _, emb := range embeddings {
		vec := bytesToFloat64Slice(emb.Embedding)
		if len(vec) == 0 {
			continue
		}
		data = append(data, embeddingDataItem{
			ID:     emb.PageID,
			Vector: vec,
		})
	}

	req := &embeddingRequest{
		Command:        "index",
		IndexPath:      s.indexPath,
		EmbeddingsData: data,
	}

	output, err := s.callPython(ctx, req)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	var resp indexResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return fmt.Errorf("decode index response: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("index error: %s", resp.Error)
	}

	log.Printf("[SemanticSearch] FAISS index rebuilt: %d vectors indexed", resp.TotalIndexed)
	return nil
}

// Search performs a semantic search and returns ranked results.
func (s *SemanticSearcher) Search(ctx context.Context, query string, topK int) ([]models.SemanticSearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, err := os.Stat(s.indexPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("FAISS index not built yet. Crawl some pages with semantic search enabled first")
	}

	model := s.config.EmbeddingModel
	if model == "" {
		model = "intfloat/multilingual-e5-large"
	}

	// E5 models require "query: " prefix for search queries
	searchQuery := query
	if isE5Model(model) {
		searchQuery = "query: " + query
	}

	req := &embeddingRequest{
		Command:   "search",
		Query:     searchQuery,
		Model:     model,
		IndexPath: s.indexPath,
		TopK:      topK,
	}

	output, err := s.callPython(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	var resp searchResponse
	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("search error: %s", resp.Error)
	}

	if len(resp.Results) == 0 {
		return nil, nil
	}

	// Fetch page details from DB
	pageIDs := make([]uint, 0, len(resp.Results))
	scoreMap := make(map[uint]float64, len(resp.Results))
	for _, hit := range resp.Results {
		pid := uint(hit.ID)
		pageIDs = append(pageIDs, pid)
		scoreMap[pid] = hit.Score
	}

	var pages []models.CrawledPage
	if err := s.db.Where("id IN ?", pageIDs).Find(&pages).Error; err != nil {
		return nil, fmt.Errorf("fetch pages: %w", err)
	}

	// Build ID->page map
	pageMap := make(map[uint]*models.CrawledPage, len(pages))
	for i := range pages {
		pageMap[pages[i].ID] = &pages[i]
	}

	// Look up domains from crawl_jobs
	jobIDs := make([]string, 0)
	jobIDSet := make(map[string]bool)
	for _, p := range pages {
		if !jobIDSet[p.CrawlJobID] {
			jobIDs = append(jobIDs, p.CrawlJobID)
			jobIDSet[p.CrawlJobID] = true
		}
	}
	domainMap := make(map[string]string)
	if len(jobIDs) > 0 {
		var jobs []models.CrawlJob
		s.db.Select("id, domain").Where("id IN ?", jobIDs).Find(&jobs)
		for _, j := range jobs {
			domainMap[j.ID] = j.Domain
		}
	}

	// Build results in the order returned by FAISS (ranked by score)
	results := make([]models.SemanticSearchResult, 0, len(resp.Results))
	for _, hit := range resp.Results {
		pid := uint(hit.ID)
		page, ok := pageMap[pid]
		if !ok {
			continue
		}
		score := math.Round(hit.Score*1000) / 1000 // 3 decimal places
		results = append(results, models.SemanticSearchResult{
			PageID:       pid,
			URL:          page.URL,
			Title:        page.Title,
			Score:        score,
			ScorePercent: score * 100,
			CrawlJobID:   page.CrawlJobID,
			Domain:       domainMap[page.CrawlJobID],
		})
	}

	return results, nil
}

// IndexPath returns the current FAISS index file path.
func (s *SemanticSearcher) IndexPath() string {
	return s.indexPath
}

// HasIndex returns true if a FAISS index file exists.
func (s *SemanticSearcher) HasIndex() bool {
	_, err := os.Stat(s.indexPath)
	return err == nil
}

// EmbeddingCount returns the total number of stored embeddings.
func (s *SemanticSearcher) EmbeddingCount() int64 {
	var count int64
	s.db.Model(&models.PageEmbedding{}).Count(&count)
	return count
}

// --- Helpers for serializing float64 slices as float32 bytes ---

func float64SliceToBytes(v []float64) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(float32(f))
		binary.LittleEndian.PutUint32(buf[i*4:], bits)
	}
	return buf
}

func bytesToFloat64Slice(b []byte) []float64 {
	if len(b)%4 != 0 {
		return nil
	}
	n := len(b) / 4
	v := make([]float64, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		v[i] = float64(math.Float32frombits(bits))
	}
	return v
}

// isE5Model returns true if the model name indicates an E5 family model,
// which requires "query: " / "passage: " prefixes for embeddings.
func isE5Model(modelName string) bool {
	lower := strings.ToLower(modelName)
	return strings.Contains(lower, "/e5-") || strings.Contains(lower, "-e5-")
}
