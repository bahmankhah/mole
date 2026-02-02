package main

import (
	"html/template"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/resolver/crawler/config"
	"github.com/resolver/crawler/crawler"
	"github.com/resolver/crawler/database"
	"github.com/resolver/crawler/handlers"
	"github.com/resolver/crawler/jobs"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Resolver Crawler...")

	// Load configuration
	cfg := config.DefaultConfig()
	log.Printf("Configuration loaded: DB=%s:%s/%s, Server=%s:%s",
		cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName,
		cfg.Server.Host, cfg.Server.Port)

	// Connect to database
	db, err := database.New(cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Run migrations
	if err := db.AutoMigrate(); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Seed default phrases
	if err := db.SeedDefaultPhrases(); err != nil {
		log.Printf("Warning: Failed to seed default phrases: %v", err)
	}

	// Create crawler engine
	engine := crawler.NewEngine(cfg.Crawler, db.DB)

	// Create job manager
	jobManager := jobs.NewManager(db.DB, cfg, engine)
	defer jobManager.Shutdown()

	// Create HTTP handlers
	handler := handlers.NewHandler(jobManager)

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	// Template functions
	funcMap := template.FuncMap{
		"divf": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
	}

	// Load templates with custom functions
	tmpl := template.Must(template.New("").Funcs(funcMap).ParseGlob("templates/*.html"))
	router.SetHTMLTemplate(tmpl)

	// Static files (if needed)
	router.Static("/static", "./static")

	// Web routes
	router.GET("/", handler.Index)
	router.GET("/jobs/:id", func(c *gin.Context) {
		c.Request.Header.Set("Accept", "text/html")
		handler.GetJob(c)
	})
	router.GET("/discovery/:id", func(c *gin.Context) {
		c.Request.Header.Set("Accept", "text/html")
		handler.GetDiscoveryJob(c)
	})

	// API routes
	api := router.Group("/api")
	{
		// Jobs
		api.POST("/jobs", handler.CreateJob)
		api.GET("/jobs", handler.GetJobs)
		api.GET("/jobs/:id", handler.GetJob)
		api.POST("/jobs/:id/start", handler.StartJob)
		api.POST("/jobs/stop", handler.StopJob)
		api.POST("/jobs/pause", handler.PauseJob)
		api.POST("/jobs/resume", handler.ResumeJob)
		api.DELETE("/jobs/:id", handler.DeleteJob)

		// Discovery jobs
		api.GET("/discovery", handler.GetDiscoveryJobs)
		api.GET("/discovery/:id", handler.GetDiscoveryJob)

		// Subdomains
		api.POST("/jobs/:id/subdomains/discover", handler.StartSubdomainDiscovery)
		api.GET("/jobs/:id/subdomains", handler.GetSubdomains)
		api.POST("/subdomains/:id/crawl", handler.StartCrawlForSubdomain)

		// Crawled pages
		api.GET("/jobs/:id/pages", handler.GetCrawledPages)

		// Phrase matches
		api.GET("/matches", handler.GetMatches)

		// Search phrases
		api.GET("/phrases", handler.GetPhrases)
		api.POST("/phrases", handler.AddPhrase)
		api.PUT("/phrases/:id", handler.UpdatePhrase)
		api.DELETE("/phrases/:id", handler.DeletePhrase)

		// Stats
		api.GET("/stats", handler.GetStats)
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Shutting down...")
		jobManager.Shutdown()
		os.Exit(0)
	}()

	// Start server
	addr := cfg.Server.Host + ":" + cfg.Server.Port
	log.Printf("Server starting on http://%s", addr)
	if err := router.Run(addr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
