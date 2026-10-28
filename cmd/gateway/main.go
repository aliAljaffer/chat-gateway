package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aliAljaffer/chat-gateway/internal/config"
	"github.com/aliAljaffer/chat-gateway/internal/opencode"
	"github.com/aliAljaffer/chat-gateway/internal/server"
	"github.com/aliAljaffer/chat-gateway/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Workspace, 0o750); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	oc := opencode.New(cfg.OpenCodeURL, cfg.OpenCodeAgent, log)
	srv, err := server.New(cfg, st, oc, log)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		log.Info("chat gateway listening", "addr", cfg.Addr, "ui_auth", uiMode(cfg), "model", cfg.Model)
		errs <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

func uiMode(cfg config.Config) string {
	if cfg.DevUI {
		return "dev"
	}
	if cfg.CFAudience == "" || cfg.CFTeamDomain == "" {
		return "disabled"
	}
	return "cloudflare-access"
}
