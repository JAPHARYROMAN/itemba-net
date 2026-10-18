// Command itemba is the single ITEMBA NET binary. The --role flag selects
// which subsystems this process runs, so one deployment can span one machine
// or three without a second codebase.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/itemba-net/core/internal/config"
	"github.com/itemba-net/core/internal/console"
	"github.com/itemba-net/core/internal/relay"
	"github.com/itemba-net/core/internal/scout"
	"github.com/itemba-net/core/internal/shield"
	"github.com/itemba-net/core/internal/vault"
)

func main() {
	roleFlag := flag.String("role", string(RoleAll), "which subsystems this process runs: console, crawler, probe or all")
	configFlag := flag.String("config", ".env.local", "optional local configuration file; process environment variables take precedence")
	staticFlag := flag.String("static", "web/dist", "directory holding the built dashboard, served when its index.html exists")
	flag.Parse()

	role, err := ParseRole(*roleFlag)
	if err != nil {
		log.Fatalf("[ITEMBA-NET] %v", err)
	}
	if phase := role.PendingPhase(); phase != 0 {
		log.Fatalf("[ITEMBA-NET] role %s has no workers yet: it arrives with Phase %d of IMPLEMENTATION-PLAN.md", role, phase)
	}

	if err := config.LoadLocal(*configFlag); err != nil {
		log.Fatalf("local configuration: %v", err)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	sh := shield.New()
	sc := scout.New(sh, baseURL)
	vlt := vault.NewManager(sh)
	defer func() {
		vlt.Destroy()
		sc.Close()
	}()

	srv := console.New(console.Dependencies{
		Shield:    sh,
		Scout:     sc,
		Vault:     vlt,
		Relay:     relay.NewHandler(sh),
		StaticDir: *staticFlag,
	})
	server := &http.Server{
		Addr:    ":" + port,
		Handler: srv.Handler(),
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("[ITEMBA-NET] Shutting down and destroying active sandboxes...")
		vlt.Destroy()
		sc.Close()
		_ = server.Close()
	}()

	log.Printf("================================================================")
	log.Printf(" ITEMBA NET Control Plane Online at %s (role: %s)", baseURL, role)
	log.Printf(" • Pillar 1: itemba-scout (Federated Crawler & Pooled CDP Verifier)")
	log.Printf(" • Pillar 2: itemba-vault (Hybrid RBI Sandbox & Zero-Trust Stream Relay)")
	log.Printf(" • Pillar 3: itemba-shield (Triple-Layer DNS/CDP/DOM Sanitizer)")
	if role == RoleAll {
		log.Printf(" • Crawler and probe workers arrive with Phases 2 and 4 of IMPLEMENTATION-PLAN.md")
	}
	log.Printf("================================================================")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
