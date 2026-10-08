package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net"
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
	restorePath := flag.String("restore", "", "Restore a NO SLEEP backup while the server is stopped")
	flag.Parse()
	addr := os.Getenv("NO_SLEEP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	demo := os.Getenv("NO_SLEEP_DEMO") != "0"
	if demo && !strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasPrefix(addr, "localhost:") && !strings.HasPrefix(addr, "[::1]:") {
		log.Fatal("Demo accounts are enabled: bind to a loopback address or set NO_SLEEP_DEMO=0.")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("Cannot start NO SLEEP at %s. Stop the earlier server with Ctrl+C in its terminal, then try again.", addr)
		return
	}
	defer listener.Close()
	project, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	dbPath, note, err := app.ResolveDatabase(project, os.Getenv("NO_SLEEP_DB"))
	if err != nil {
		log.Fatal(err)
	}
	if note != "" {
		log.Print(note)
	}
	log.Printf("NO SLEEP database: %s", dbPath)
	if *restorePath != "" {
		previous, err := app.RestoreDatabase(dbPath, *restorePath)
		if err != nil {
			log.Fatal(err)
		}
		log.Print("Backup restored. Run go run . to start NO SLEEP.")
		if previous != "" {
			log.Printf("Previous database retained at %s", previous)
		}
		return
	}
	if os.Getenv("NO_SLEEP_DB") == "" && dbPath == filepath.Join(project, "data", "runtime.db") {
		if err := app.PrepareRuntimeDatabase(dbPath, filepath.Join(project, "data", "nosleep.db")); err != nil {
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
	store.Backups = app.NewBackupManager(store, filepath.Join(project, "data", "backups"))
	store.Backups.Start(ctx)
	defer func() { stop(); store.Backups.Wait() }()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	log.Printf("NO SLEEP is ready at http://%s", addr)
	if demo {
		log.Print("Local demo accounts enabled. Payment recording does not transfer money.")
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Print(err)
		stop()
	}
	<-shutdownDone
}
