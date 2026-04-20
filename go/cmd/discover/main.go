package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/hecate/navidrome-jukebox/internal/upnp"
)

func main() {
	rendererName := flag.String("name", "", "UPnP renderer name prefix to match (e.g., RX-A4A)")
	flag.Parse()

	if *rendererName == "" {
		log.Fatal("-name flag is required")
	}

	client := upnp.NewClient(*rendererName)
	err := client.Discover()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Discovery failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Discovered renderer: %s\n", *rendererName)
	os.Exit(0)
}
