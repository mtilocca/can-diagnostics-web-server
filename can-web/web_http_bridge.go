package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"time"
)

func StartWebServer(ctx context.Context, addr string, iface string, store *Store) error {
	mux := http.NewServeMux()

	// Static UI
	webDir := filepath.Join(".", "web")
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	// API endpoint
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		signals, raw := store.Snapshot()
		resp := map[string]any{
			"ts":      time.Now().UTC(),
			"iface":   iface,
			"signals": signals,
			"raw":     raw,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shutdown on ctx cancel
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("Web: http://%s", addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
