package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/config"
	"github.com/yet-an-other/xform/internal/hoststats"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

// version is stamped at release time via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load configuration", "error", err)
		os.Exit(1)
	}

	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hostStats := hoststats.NewCache(hoststats.NewCollector(), 5*time.Second)
	hostStats.Start(shutdownSignal)
	xrayStatus := xraystatus.NewCache(
		xraystatus.NewCollector(xraystatus.SystemdUnit{}, xraystatus.BinaryVersion{}, cfg.XrayUnitName),
		5*time.Second,
	)
	xrayStatus.Start(shutdownSignal)
	sessions := session.NewManager(cfg.Password, time.Now)

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           newHandler(hostStats, xrayStatus, sessions),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-shutdownSignal.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			slog.Error("shut down HTTP server", "error", err)
		}
	}()

	slog.Info("xform listening",
		"version", version,
		"address", cfg.ListenAddress,
		"xray_api", cfg.XrayAPIAddress,
		"xray_config", cfg.XrayConfigPath,
		"db", cfg.DBPath,
		"xray_unit", cfg.XrayUnitName,
		"password_set", cfg.Password != "",
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve xform", "error", err)
		os.Exit(1)
	}
}

func newHandler(snapshots *hoststats.Cache, statuses *xraystatus.Cache, sessions *session.Manager) http.Handler {
	return api.New(snapshots, statuses, sessions, newDashboardHandler())
}
