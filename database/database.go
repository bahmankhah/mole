package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

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
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.DBName,
	)

	logLevel := logger.Warn
	if os.Getenv("MOLE_SQL_DEBUG") != "" {
		logLevel = logger.Info
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
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

// WithLock runs fn while holding the MySQL named lock name, waiting up to wait
// for it. It reports whether the lock was acquired; fn does not run otherwise.
// Named locks are server-wide and released automatically if the connection
// drops, so they coordinate every process sharing the database.
func WithLock(db *gorm.DB, name string, wait time.Duration, fn func() error) (bool, error) {
	acquired := false
	err := db.Connection(func(conn *gorm.DB) error {
		// Waiting for the lock is expected; keep it out of the slow-query log.
		quiet := conn.Session(&gorm.Session{Logger: conn.Logger.LogMode(logger.Silent)})
		var got sql.NullInt64
		if err := quiet.Raw("SELECT GET_LOCK(?, ?)", name, int(wait/time.Second)).Scan(&got).Error; err != nil {
			return fmt.Errorf("lock %s: %w", name, err)
		}
		if !got.Valid || got.Int64 != 1 {
			return nil
		}
		acquired = true
		defer func() {
			var released sql.NullInt64
			conn.Raw("SELECT RELEASE_LOCK(?)", name).Scan(&released)
		}()
		return fn()
	})
	return acquired, err
}

// AutoMigrate runs the schema migrations. A named lock makes processes that
// start together migrate one after the other instead of racing.
func (d *Database) AutoMigrate() error {
	const wait = 5 * time.Minute
	acquired, err := WithLock(d.DB, "mole:migrate", wait, d.migrate)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("another process held the migration lock for %s; try again", wait)
	}
	return nil
}

func (d *Database) migrate() error {
	log.Println("Running database migrations...")

	err := d.DB.AutoMigrate(
		&models.DiscoveryJob{},
		&models.PortScanJob{},
		&models.DiscoveredPort{},
		&models.CrawlJob{},
		&models.Subdomain{},
		&models.CrawledPage{},
		&models.FrontierURL{},
		&models.SearchPhrase{},
		&models.PhraseMatch{},
		&models.PageEmbedding{},
		&models.PageContent{},
		&models.PageTag{},
		&models.PageChange{},
		&models.Schedule{},
		&models.WebhookDelivery{},
		&models.IdempotencyKey{},
		&models.WorkerHeartbeat{},
		&models.WorkerControl{},
		&models.JoinToken{},
		&models.OwnerLimit{},
		&models.ScrapeCacheEntry{},
		&models.ProductGroup{},
		&models.ProductGroupMember{},
		&models.FieldObservation{},
	)
	if err != nil {
		return err
	}

	d.dropObsoleteGroupingColumns()

	// Create additional composite index for search performance (ignore if exists)
	if !d.indexExists("phrase_matches", "idx_phrase_match_phrase_url") {
		d.DB.Exec("CREATE INDEX idx_phrase_match_phrase_url ON phrase_matches(phrase(255), url(255))")
	}

	// Create composite index for doc_hash + crawl_job_id deduplication (ignore if exists)
	if !d.indexExists("crawled_pages", "idx_doc_crawl_job") {
		d.DB.Exec("CREATE INDEX idx_doc_crawl_job ON crawled_pages(doc_hash, crawl_job_id)")
	}

	// A job's kept pages in id order: page lists, "load more" cursors and
	// title/URL search walk this index and stop once a page of results is found.
	if !d.indexExists("crawled_pages", "idx_crawled_pages_job_id") {
		d.DB.Exec("CREATE INDEX idx_crawled_pages_job_id ON crawled_pages(crawl_job_id, is_archived, skipped, id)")
	}

	// Queue claim order: highest priority first, then oldest.
	if !d.indexExists("crawl_jobs", "idx_crawl_jobs_queue") {
		d.DB.Exec("CREATE INDEX idx_crawl_jobs_queue ON crawl_jobs(status, priority, queued_at)")
	}

	// Migration: drop the old global unique index on url_hash if it exists,
	// since it's now a composite unique index (crawl_job_id, url_hash).
	if d.indexExists("crawled_pages", "idx_crawled_pages_url_hash") {
		d.DB.Exec("DROP INDEX idx_crawled_pages_url_hash ON crawled_pages")
	}

	return nil
}

// indexExists checks whether an index exists on a given table in the current database.
// dropObsoleteGroupingColumns removes the columns left by the first shape
// of product grouping, where a group belonged to a job group rather than to
// a scope (a crawl, a schedule or a comparison). AutoMigrate adds columns
// but never drops them, and the old ones are NOT NULL without a default, so
// every insert into product_groups fails until they are gone.
func (d *Database) dropObsoleteGroupingColumns() {
	obsolete := []struct{ table, column string }{
		{"product_groups", "job_group_id"},
		{"product_groups", "lowest_price"},
		{"product_groups", "highest_price"},
		{"product_group_members", "job_group_id"},
		{"product_group_members", "group_site_id"},
	}
	for _, o := range obsolete {
		if !d.columnExists(o.table, o.column) {
			continue
		}
		if err := d.DB.Exec(fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`", o.table, o.column)).Error; err != nil {
			log.Printf("[Migrate] Could not drop obsolete column %s.%s: %v", o.table, o.column, err)
			continue
		}
		log.Printf("[Migrate] Dropped obsolete column %s.%s", o.table, o.column)
	}
	// price_observations became field_observations, which records any
	// compared field rather than only a price. Drop it only while empty,
	// so no observation anyone kept is thrown away.
	if d.tableExists("price_observations") {
		var rows int64
		d.DB.Raw("SELECT COUNT(*) FROM price_observations").Scan(&rows)
		if rows == 0 {
			if err := d.DB.Exec("DROP TABLE price_observations").Error; err == nil {
				log.Printf("[Migrate] Dropped empty obsolete table price_observations (replaced by field_observations)")
			}
		} else {
			log.Printf("[Migrate] price_observations holds %d rows and is no longer used; drop it by hand once you no longer want them", rows)
		}
	}
}

func (d *Database) columnExists(table, column string) bool {
	var count int64
	d.DB.Raw(
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		table, column,
	).Scan(&count)
	return count > 0
}

func (d *Database) tableExists(table string) bool {
	var count int64
	d.DB.Raw(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		table,
	).Scan(&count)
	return count > 0
}

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
