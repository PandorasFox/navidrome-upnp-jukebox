package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/hecate/navidrome-jukebox/internal/server"
)

func main() {
	// Config flags (override environment variables)
	navidromeURL := flag.String("navidrome", getEnv("NAVIDROME_URL", "http://localhost:4533"), "Navidrome base URL")
	navidromeUser := flag.String("navidrome-user", getEnv("NAVIDROME_USER", ""), "Navidrome username")
	navidromePass := flag.String("navidrome-pass", getEnv("NAVIDROME_PASS", ""), "Navidrome password")
	rendererName := flag.String("renderer", getEnv("RENDERER_NAME", ""), "UPnP renderer name prefix for discovery (e.g., RX-A4A)")
	listenAddr := flag.String("listen", getEnv("LISTEN", ":8080"), "Listen address")
	lastfmKey := flag.String("lastfm-key", getEnv("LASTFM_API_KEY", ""), "Last.fm API key (optional, enables scrobbling)")
	lastfmSecret := flag.String("lastfm-secret", getEnv("LASTFM_API_SECRET", ""), "Last.fm API secret")
	flag.Parse()

	// Require renderer name
	if *rendererName == "" {
		log.Fatal("Renderer name required. Use -renderer flag or RENDERER_NAME env var")
	}

	// Create server
	app, err := server.NewServer(*navidromeURL, *navidromeUser, *navidromePass, *rendererName, *lastfmKey, *lastfmSecret)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Start playback loop
	app.StartPlaybackLoop()

	// Start library sync in background
	go func() {
		ctx := context.Background()
		if err := app.SyncLibrary(ctx); err != nil {
			log.Printf("Library sync failed: %v", err)
		}
	}()

	// Connect to UPnP renderer in background
	go func() {
		if err := app.StartUPnP(); err != nil {
			log.Printf("Warning: Failed to connect to UPnP renderer: %v", err)
			log.Printf("Playback features will be unavailable until renderer is reachable")
		}
	}()

	// Set up routes
	r := app.Routes()

	log.Printf("Starting jukebox server on %s", *listenAddr)
	log.Printf("Navidrome: %s", *navidromeURL)
	log.Printf("Renderer name: %s", *rendererName)
	if *lastfmKey != "" {
		log.Printf("Last.fm scrobbling: enabled")
	} else {
		log.Printf("Last.fm scrobbling: disabled (set LASTFM_API_KEY to enable)")
	}

	if err := http.ListenAndServe(*listenAddr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
