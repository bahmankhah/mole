package database

import (
	"fmt"
	"log"

	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Database wraps the GORM database connection
type Database struct {
	DB *gorm.DB
}

// New creates a new database connection
func New(cfg config.DatabaseConfig) (*Database, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.DBName,
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Get underlying SQL DB and configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying DB: %w", err)
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)

	return &Database{DB: db}, nil
}

// AutoMigrate runs automatic migrations for all models
func (d *Database) AutoMigrate() error {
	log.Println("Running database migrations...")

	err := d.DB.AutoMigrate(
		&models.DiscoveryJob{},
		&models.CrawlJob{},
		&models.Subdomain{},
		&models.CrawledPage{},
		&models.FrontierURL{},
		&models.SearchPhrase{},
		&models.PhraseMatch{},
		&models.PageEmbedding{},
	)
	if err != nil {
		return err
	}

	// Create additional composite index for search performance (ignore if exists)
	if !d.indexExists("phrase_matches", "idx_phrase_match_phrase_url") {
		d.DB.Exec("CREATE INDEX idx_phrase_match_phrase_url ON phrase_matches(phrase(255), url(255))")
	}

	// Create composite index for doc_hash + crawl_job_id deduplication (ignore if exists)
	if !d.indexExists("crawled_pages", "idx_doc_crawl_job") {
		d.DB.Exec("CREATE INDEX idx_doc_crawl_job ON crawled_pages(doc_hash, crawl_job_id)")
	}

	// Migration: drop the old global unique index on url_hash if it exists,
	// since it's now a composite unique index (crawl_job_id, url_hash).
	if d.indexExists("crawled_pages", "idx_crawled_pages_url_hash") {
		d.DB.Exec("DROP INDEX idx_crawled_pages_url_hash ON crawled_pages")
	}

	return nil
}

// indexExists checks whether an index exists on a given table in the current database.
func (d *Database) indexExists(table, indexName string) bool {
	var count int64
	d.DB.Raw(
		"SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		table, indexName,
	).Scan(&count)
	return count > 0
}

// SeedDefaultPhrases inserts default search phrases if not exist
func (d *Database) SeedDefaultPhrases() error {
	defaultPhrases := []string{}

	for _, phrase := range defaultPhrases {
		var existing models.SearchPhrase
		result := d.DB.Where("phrase = ?", phrase).First(&existing)
		if result.Error == gorm.ErrRecordNotFound {
			if err := d.DB.Create(&models.SearchPhrase{Phrase: phrase, IsActive: true}).Error; err != nil {
				return err
			}
			log.Printf("Added default search phrase: %s", phrase)
		}
	}
	return nil
}

// Close closes the database connection
func (d *Database) Close() error {
	sqlDB, err := d.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
