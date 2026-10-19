// Command api serves the dashboard and its read-only JSON API.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/api"
	"git.subcult.tv/subculture-collective/sus-quotient/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Error("database url", "err", err)
		os.Exit(2)
	}
	cfg.MaxConns = 8
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	trusted, err := api.ParsePrefixes(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		log.Error("TRUSTED_PROXIES", "err", err)
		os.Exit(2)
	}
	server := api.New(db, web.FS(), log)
	server.Limit(envInt("RATE_PER_MINUTE", 120), envInt("RATE_BURST", 60), trusted)
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("api listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}
