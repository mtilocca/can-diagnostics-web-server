package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	iface := getenv("CAN_IFACE", "vcan0")
	addr := getenv("HTTP_ADDR", "127.0.0.1:8080")
	mapPath := getenv("CAN_MAP", "can_map.csv")

	frames, err := LoadCANMap(mapPath)
	if err != nil {
		log.Fatalf("failed to load can map: %v", err)
	}

	store := NewStore(200)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stop on Ctrl+C
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		cancel()
	}()

	// Start CAN reader
	go func() {
		if err := RunCANReader(ctx, iface, frames, store); err != nil {
			log.Printf("CAN reader stopped: %v", err)
			cancel()
		}
	}()

	// Start web server (blocks)
	if err := StartWebServer(ctx, addr, iface, store); err != nil {
		log.Fatalf("web server error: %v", err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
