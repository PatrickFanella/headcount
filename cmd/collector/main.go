// Command collector discovers top Twitch channels, tracks them indefinitely,
// and records viewer, chat and follower data. It also scores finished streams.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/chat"
	"git.subcult.tv/subculture-collective/headcount/internal/helix"
	"git.subcult.tv/subculture-collective/headcount/internal/live"
	"git.subcult.tv/subculture-collective/headcount/internal/score"
	"git.subcult.tv/subculture-collective/headcount/internal/store"
	"git.subcult.tv/subculture-collective/headcount/internal/tracker"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func main() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	clientID, secret := os.Getenv("TWITCH_CLIENT_ID"), os.Getenv("TWITCH_CLIENT_SECRET")
	dbURL := os.Getenv("DATABASE_URL")
	if clientID == "" || secret == "" || dbURL == "" {
		log.Error("TWITCH_CLIENT_ID, TWITCH_CLIENT_SECRET and DATABASE_URL are required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var st *store.Store
	var err error
	for attempt := range 30 {
		st, err = store.Open(ctx, dbURL)
		if err == nil {
			break
		}
		log.Warn("database not ready", "attempt", attempt, "err", err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	if pw := os.Getenv("API_DB_PASSWORD"); pw != "" {
		if err := st.EnableAPIRole(ctx, pw); err != nil {
			log.Error("enable api role", "err", err)
			os.Exit(1)
		}
	}

	h := helix.New(clientID, secret)
	ls := live.New()
	poller := tracker.New(tracker.Config{
		TopN:      envInt("TOP_N", 200),
		EndGrace:  time.Duration(envInt("END_GRACE_MINUTES", 4)) * time.Minute,
		ColdEvery: max(1, envInt("COLD_EVERY_MINUTES", 10)),
	}, h, st, ls, log.With("component", "tracker"))
	if err := poller.Init(ctx); err != nil {
		log.Error("init tracker", "err", err)
		os.Exit(1)
	}
	if seeds := os.Getenv("SEED_LOGINS"); seeds != "" {
		if err := seed(ctx, h, st, strings.Split(seeds, ",")); err != nil {
			log.Warn("seed channels", "err", err)
		}
	}
	collector := chat.New(ls, st, h, log.With("component", "chat"))
	scorer := score.New(st, log.With("component", "score"))
	scorer.ChatterRetention = time.Duration(envInt("CHATTER_RETENTION_DAYS", 30)) * 24 * time.Hour

	go poller.Run(ctx)
	go collector.Run(ctx)
	go scorer.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		lastOK, liveN, polled := poller.Health()
		joined, lastFlush, lastMsgs := collector.Health()
		reqs, fails, remaining := h.Stats()
		healthy := time.Since(lastOK) < 3*time.Minute
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"healthy": healthy, "last_poll": lastOK, "live": liveN, "polled": polled,
			"chat_joined": joined, "chat_last_flush": lastFlush, "chat_last_minute_messages": lastMsgs,
			"helix_requests": reqs, "helix_failures": fails, "helix_remaining": remaining,
		})
	})
	srv := &http.Server{Addr: env("HEALTH_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "err", err)
		}
	}()
	log.Info("collector started")

	// Exit if polling has stalled, so Docker's restart policy recovers it.
	// The first poll comes within about a minute of startup.
	started := time.Now()
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			lastOK, _, _ := poller.Health()
			if lastOK.Before(started) {
				lastOK = started
			}
			if time.Since(lastOK) > 10*time.Minute {
				log.Error("no successful poll in 10 minutes; exiting for restart", "last_ok", lastOK)
				os.Exit(1)
			}
		}
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	log.Info("collector stopped")
}

// seed adds named channels to tracking before they reach the top list.
func seed(ctx context.Context, h *helix.Client, st *store.Store, logins []string) error {
	var clean []string
	for _, l := range logins {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			clean = append(clean, l)
		}
	}
	for start := 0; start < len(clean); start += 100 {
		users, err := h.UsersByLogin(ctx, clean[start:min(start+100, len(clean))])
		if err != nil {
			return err
		}
		entries := make([]store.TopEntry, 0, len(users))
		for _, u := range users {
			id, err := strconv.ParseInt(u.ID, 10, 64)
			if err != nil {
				continue
			}
			entries = append(entries, store.TopEntry{ID: id, Login: u.Login, DisplayName: u.DisplayName})
		}
		if _, err := st.SeedChannels(ctx, time.Now().UTC().Truncate(time.Minute), entries); err != nil {
			return err
		}
	}
	return nil
}
