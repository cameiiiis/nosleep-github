package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"nosleep/internal/app"
)

//go:embed web/*
var assets embed.FS

func main() {
	addr := os.Getenv("NO_SLEEP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	demo := os.Getenv("NO_SLEEP_DEMO") != "0"
	if demo && !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") && !strings.HasPrefix(addr, "[::1]:") {
		log.Fatal("Demo accounts are enabled: bind to a loopback address or set NO_SLEEP_DEMO=0.")
	}
	dbPath := os.Getenv("NO_SLEEP_DB")
	if dbPath == "" {
		dbPath = filepath.Join("data", "runtime.db")
		if err := app.PrepareRuntimeDatabase(dbPath, filepath.Join("data", "nosleep.db")); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		log.Fatal(err)
	}
	store, err := app.OpenStore(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	web, err := fs.Sub(assets, "web")
	if err != nil {
		log.Fatal(err)
	}
	handler, err := app.NewHandler(store, web, demo)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("NO SLEEP is ready at http://%s", addr)
	if demo {
		log.Print("Local demo accounts enabled. Payment recording does not transfer money.")
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
