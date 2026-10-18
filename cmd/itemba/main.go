// Command itemba is the single ITEMBA NET binary. The --role flag selects
// which subsystems this process runs, so one deployment can span one machine
// or three without a second codebase.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/config"
	"github.com/itemba-net/core/internal/console"
	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/metrics"
	"github.com/itemba-net/core/internal/relay"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

func main() {
	roleFlag := flag.String("role", string(RoleAll), "which subsystems this process runs: console, crawler, probe or all")
	configFlag := flag.String("config", ".env.local", "optional local configuration file; process environment variables take precedence")
	staticFlag := flag.String("static", "web/dist", "directory holding the built dashboard, served when its index.html exists")
	databaseFlag := flag.String("database", "", "PostgreSQL URL for Atlas; overrides ITEMBA_DATABASE_URL. Empty runs without persistence")
	driversFlag := flag.String("drivers", "drivers", "directory of host driver definitions for the crawler")
	flag.Parse()

	role, err := ParseRole(*roleFlag)
	if err != nil {
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	if phase := role.PendingPhase(); phase != 0 {
		log.Fatalf("[ITEMBA-NET] role %s has no workers yet: it arrives with Phase %d of IMPLEMENTATION-PLAN.md", role, phase)
	}

	cfg, err := config.Load(*configFlag)
	if err != nil {
		log.Fatalf("[ITEMBA-NET] local configuration: %v", err)
	}
	if *databaseFlag != "" {
		cfg.DatabaseURL = *databaseFlag
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", cfg.Port)

	store := openAtlas(cfg.DatabaseURL)
	defer store.Close()

	sh := shield.New()
	sc := scout.New(sh, baseURL)
	vlt := vault.NewManager(sh)
	defer func() {
		vlt.Destroy()
		sc.Close()
	}()

	measures := metrics.New()
	measures.RegisterShield(sh)
	measures.RegisterVault(vlt)
	measures.RegisterAtlas(store)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// The host drivers serve both the console's discovery burst and the
	// crawler, so every role loads them. A broken driver set is fatal.
	drivers := loadDrivers(*driversFlag)
	sc.UseDrivers(drivers)

	var crawl *crawler.Crawler
	if role.RunsCrawler() {
		crawl = openCrawler(role, store, drivers, measures)
		if crawl != nil {
			go crawl.Run(ctx)
		}
	}

	var server *http.Server
	if role.ServesConsole() {
		srv := console.New(console.Dependencies{
			Shield:    sh,
			Scout:     sc,
			Vault:     vlt,
			Relay:     relay.NewHandler(sh),
			Atlas:     store,
			Metrics:   measures,
			Crawler:   crawl,
			StaticDir: *staticFlag,
		})
		server = &http.Server{Addr: ":" + cfg.Port, Handler: srv.Handler()}
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("[ITEMBA-NET] Shutting down and destroying active sandboxes...")
		stop()
		vlt.Destroy()
		sc.Close()
		store.Close()
		if server != nil {
			_ = server.Close()
		}
	}()

	log.Printf("================================================================")
	log.Printf(" ITEMBA NET Control Plane Online at %s (role: %s)", baseURL, role)
	log.Printf(" • Pillar 1: itemba-scout (Federated Crawler & Pooled CDP Verifier)")
	log.Printf(" • Pillar 2: itemba-vault (Hybrid RBI Sandbox & Zero-Trust Stream Relay)")
	log.Printf(" • Pillar 3: itemba-shield (Triple-Layer DNS/CDP/DOM Sanitizer)")
	if server != nil {
		log.Printf(" • Metrics at %s/metrics", baseURL)
	}
	if crawl != nil {
		log.Printf(" • Crawler running from %s; status at %s/api/crawler/status", *driversFlag, baseURL)
	}
	log.Printf(" • Probe workers arrive with Phase 4 of IMPLEMENTATION-PLAN.md")
	log.Printf("================================================================")

	if server == nil {
		// Crawler-only process: run until a signal arrives.
		<-ctx.Done()
		return
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

// openAtlas connects to the database and applies pending migrations. A
// configured database that cannot be reached or migrated is fatal: a process
// must not run while silently forgetting everything. No URL means no
// persistence, stated once at startup.
func openAtlas(url string) *atlas.Store {
	if url == "" {
		log.Printf("[ITEMBA-NET] atlas: no database configured; nothing is persisted (set ITEMBA_DATABASE_URL or --database)")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := atlas.Open(ctx, url)
	if err != nil {
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelMigrate()
	applied, err := store.Migrate(migrateCtx)
	if err != nil {
		store.Close()
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	health := store.Health(migrateCtx)
	log.Printf("[ITEMBA-NET] atlas: connected to PostgreSQL %s; %d migration(s) applied now %v, %d in total", health.ServerVersion, len(applied), applied, health.Migrations)
	return store
}

// loadDrivers reads the host drivers every role searches with. A broken or
// empty driver set is fatal: a process searching with half its hosts would
// quietly skew every result.
func loadDrivers(dir string) []*crawler.Driver {
	drivers, err := crawler.LoadDir(dir)
	if err != nil {
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	if len(drivers) == 0 {
		log.Fatalf("[ITEMBA-NET] no drivers found in %s", dir)
	}
	names := make([]string, 0, len(drivers))
	for _, d := range drivers {
		names = append(names, d.Name)
	}
	log.Printf("[ITEMBA-NET] drivers: %d loaded from %s %v", len(drivers), dir, names)
	return drivers
}

// openCrawler builds the crawler over the loaded drivers. The crawler role
// cannot run without a database; the all role runs its console and says the
// crawler is idle.
func openCrawler(role Role, store *atlas.Store, drivers []*crawler.Driver, measures *metrics.Metrics) *crawler.Crawler {
	if store == nil {
		if role == RoleCrawler {
			log.Fatalf("[ITEMBA-NET] the crawler needs a database: set ITEMBA_DATABASE_URL or --database")
		}
		log.Printf("[ITEMBA-NET] crawler idle: no database configured")
		return nil
	}
	crawl, err := crawler.New(crawler.Options{Store: store, Drivers: drivers, Observer: measures})
	if err != nil {
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	return crawl
}
