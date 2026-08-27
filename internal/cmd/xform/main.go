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

	"github.com/yet-an-other/xform/internal/advertisements"
	"github.com/yet-an-other/xform/internal/api"
	"github.com/yet-an-other/xform/internal/config"
	"github.com/yet-an-other/xform/internal/geoip"
	"github.com/yet-an-other/xform/internal/hoststats"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
	"github.com/yet-an-other/xform/internal/xraygrpc"
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

	store, err := users.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	hostStats := hoststats.NewCache(hoststats.NewCollector(), 5*time.Second)
	hostStats.Start(shutdownSignal)
	statsAPI := xraygrpc.Client{Address: cfg.XrayAPIAddress}
	xrayStatus := xraystatus.NewCache(
		xraystatus.NewCollector(
			xraystatus.SystemdUnit{},
			xraystatus.BinaryVersion{},
			statsAPI,
			cfg.XrayUnitName,
		).WithTotalsStore(store),
		5*time.Second,
	)
	xrayStatus.Start(shutdownSignal)
	configWatcher := xrayconfig.NewWatcher(cfg.XrayConfigPath)
	configWatcher.Start(shutdownSignal)
	advertisementWatcher := advertisements.NewWatcher(cfg.ConnectionsConfigPath).WithInbounds(configWatcher)
	advertisementWatcher.Start(shutdownSignal)
	usersCollector := users.NewCollector(statsAPI, statsAPI, store).WithRoster(configWatcher)
	if geo := loadGeoIP(cfg); geo != nil {
		usersCollector.WithGeo(geo)
	}
	usersCache := users.NewCache(
		usersCollector,
		5*time.Second,
	)
	usersCache.Start(shutdownSignal)
	sessions := session.NewManager(cfg.Password, time.Now)

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           newHandler(hostStats, xrayStatus, usersCache, sessions, cfg),
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

func newHandler(snapshots *hoststats.Cache, statuses *xraystatus.Cache, usersCache *users.Cache, sessions *session.Manager, cfg config.Config) http.Handler {
	panel := api.PanelInfo{Version: version, XrayAPIEndpoint: cfg.XrayAPIAddress}
	return api.New(snapshots, statuses, usersCache, sessions, newDashboardHandler(), panel)
}

// loadGeoIP opens the geoip.dat behind the users table's country flags
// (ADR-0005): XFORM_GEOIP when set, else the well-known xray asset paths.
// The feature is optional — a missing or unreadable file disables flags,
// never the panel.
func loadGeoIP(cfg config.Config) *geoip.Resolver {
	path := cfg.GeoIPPath
	if path == "" {
		candidates := []string{
			"/usr/local/share/xray/geoip.dat",
			filepath.Join(filepath.Dir(cfg.XrayConfigPath), "geoip.dat"),
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		slog.Info("geoip.dat not found; country flags disabled")
		return nil
	}
	resolver, err := geoip.Load(path)
	if err != nil {
		slog.Warn("cannot load geoip.dat; country flags disabled", "path", path, "error", err)
		return nil
	}
	slog.Info("geoip loaded; country flags enabled", "path", path)
	return resolver
}
