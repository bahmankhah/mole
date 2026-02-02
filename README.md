# Resolver Crawler

An advanced web crawler tool with subdomain discovery, phrase detection, and a clean web UI. Built with Go, GORM, MySQL, and Gin.

## Features

### Core Crawler Features
- **Random Surfer Model**: Implements the random surfer strategy with configurable teleportation probability (default 0.2)
- **URL Normalization**: Removes tracking parameters, normalizes paths, handles redirects
- **Exact Duplicate Detection**: Hash-based deduplication to avoid re-crawling URLs
- **Politeness**: Configurable delays between requests
- **Depth Limiting**: Maximum crawl depth configuration
- **Concurrent Workers**: Multiple workers for parallel crawling

### Subdomain Discovery
- DNS enumeration with common subdomain wordlist
- Discovered subdomains automatically added to frontier
- Real-time discovery updates

### Phrase Detection
- Configurable phrases to search for in crawled content
- Context extraction around matches
- Real-time notifications when phrases are found
- Default phrase: "qrmenu.com"

### Seed URL Generation
- Automatic seed URLs: `robots.txt`, `sitemap.xml`, `sitemap_index.xml`
- Subdomains added as additional seeds
- Sitemap parsing for URL extraction
- Robots.txt parsing for additional sitemaps

### Modular Architecture
All components are designed as separate modules for easy extension:
- `URLCleaner` - URL normalization and cleaning
- `ExactDuplicateDetector` - Duplicate URL detection
- `HTMLLinkExtractor` - Link extraction from HTML
- `SimplePhraseDetector` - Phrase detection in content
- `RandomSurferFrontier` - Frontier queue with random surfer model
- `SubdomainScanner` - DNS-based subdomain discovery
- `SitemapParser` - Sitemap XML parsing
- `RobotsParser` - Robots.txt parsing

## Installation

### Prerequisites
- Go 1.21+
- MySQL 8.0+
- Docker & Docker Compose (optional)

### Using Docker Compose (Recommended)

```bash
# Clone and navigate to the project
cd /var/www/go/resolver

# Start services
docker-compose up -d

# View logs
docker-compose logs -f
```

The UI will be available at `http://localhost:8080`

### Manual Installation

1. **Setup MySQL Database**
```bash
mysql -u root -p -e "CREATE DATABASE crawler_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
```

2. **Configure Environment Variables**
```bash
export DB_HOST=localhost
export DB_PORT=3306
export DB_USER=root
export DB_PASSWORD=your_password
export DB_NAME=crawler_db
export SERVER_HOST=0.0.0.0
export SERVER_PORT=8080
```

3. **Build and Run**
```bash
go mod download
go build -o crawler
./crawler
```

## Usage

### Web UI

1. Open `http://localhost:8080` in your browser
2. Enter a domain (e.g., `example.com`) and click "Create Job"
3. Click "Discover Subdomains" to scan for subdomains
4. Click "Start Crawl" to begin crawling
5. Monitor progress and view phrase matches in real-time

### API Endpoints

#### Jobs
- `POST /api/jobs` - Create a new crawl job
- `GET /api/jobs` - List all jobs
- `GET /api/jobs/:id` - Get job details
- `POST /api/jobs/:id/start` - Start a job
- `POST /api/jobs/stop` - Stop current job
- `POST /api/jobs/pause` - Pause current job
- `POST /api/jobs/resume` - Resume current job
- `DELETE /api/jobs/:id` - Delete a job

#### Subdomains
- `POST /api/jobs/:id/subdomains/discover` - Start subdomain discovery
- `GET /api/jobs/:id/subdomains` - Get discovered subdomains

#### Phrase Matches
- `GET /api/matches` - Get all phrase matches
- `GET /api/matches?job_id=xxx` - Get matches for a specific job

#### Search Phrases
- `GET /api/phrases` - List search phrases
- `POST /api/phrases` - Add a new phrase
- `PUT /api/phrases/:id` - Update phrase (enable/disable)
- `DELETE /api/phrases/:id` - Delete a phrase

#### Stats
- `GET /api/stats` - Get current crawler statistics

### Example API Usage

```bash
# Create a job
curl -X POST http://localhost:8080/api/jobs \
  -H "Content-Type: application/json" \
  -d '{"domain": "example.com", "max_depth": 5}'

# Start subdomain discovery
curl -X POST http://localhost:8080/api/jobs/{job_id}/subdomains/discover

# Start crawling
curl -X POST http://localhost:8080/api/jobs/{job_id}/start

# Add a search phrase
curl -X POST http://localhost:8080/api/phrases \
  -H "Content-Type: application/json" \
  -d '{"phrase": "contact@example.com"}'

# Get stats
curl http://localhost:8080/api/stats
```

## Configuration

Configuration is done via environment variables or defaults in `config/config.go`:

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_HOST` | localhost | MySQL host |
| `DB_PORT` | 3306 | MySQL port |
| `DB_USER` | root | MySQL user |
| `DB_PASSWORD` | (empty) | MySQL password |
| `DB_NAME` | crawler_db | Database name |
| `SERVER_HOST` | 0.0.0.0 | Server host |
| `SERVER_PORT` | 8080 | Server port |

### Crawler Settings (in code)
- `MaxConcurrentRequests`: 10 - Number of concurrent workers
- `RequestTimeout`: 30s - HTTP request timeout
- `PolitenessDelay`: 1s - Delay between requests
- `TeleportProbability`: 0.2 - Random surfer teleport probability
- `MaxDepth`: 10 - Maximum crawl depth

## Architecture

```
resolver/
├── config/           # Configuration management
├── crawler/          # Core crawler engine
├── database/         # Database connection and migrations
├── handlers/         # HTTP handlers
├── jobs/             # Job management
├── models/           # Database models
├── modules/          # Reusable modules
│   ├── interfaces.go       # Module interfaces
│   ├── url_cleaner.go      # URL normalization
│   ├── duplicate_detector.go # Duplicate detection
│   ├── link_extractor.go   # Link extraction
│   ├── phrase_detector.go  # Phrase detection
│   ├── frontier.go         # Random surfer frontier
│   └── subdomain_scanner.go # Subdomain discovery
├── templates/        # HTML templates
├── main.go           # Application entry point
├── Dockerfile
├── docker-compose.yml
└── Makefile
```

## Extending the Crawler

### Adding a New Module

1. Implement the appropriate interface from `modules/interfaces.go`
2. Create a new file in the `modules/` directory
3. Add initialization in `crawler/engine.go`

Example: Adding a near-duplicate detector:

```go
// modules/near_duplicate_detector.go
type NearDuplicateDetector struct {
    // SimHash or MinHash implementation
}

func (n *NearDuplicateDetector) Name() string {
    return "near_duplicate_detector"
}

func (n *NearDuplicateDetector) Initialize() error {
    // Setup
    return nil
}

func (n *NearDuplicateDetector) IsSimilar(content1, content2 string, threshold float64) bool {
    // Implement similarity check
    return false
}
```

### Adding New Phrase Detection Logic

Modify `modules/phrase_detector.go` or create a new implementation of `PhraseDetector` interface.

### Custom Link Extraction

Implement `LinkExtractor` interface for custom extraction logic (e.g., JavaScript-rendered content).

## License

MIT License
