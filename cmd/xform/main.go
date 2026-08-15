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

	"github.com/yet-an-other/xform/internal/dashboard"
	"github.com/yet-an-other/xform/internal/hoststats"
	"github.com/yet-an-other/xform/internal/httpapi"
)

const defaultListenAddress = "127.0.0.1:9090"

func main() {
	listenAddress := os.Getenv("XFORM_LISTEN")
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}

	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hostStats := hoststats.NewCache(hoststats.NewCollector(), 5*time.Second)
	hostStats.Start(shutdownSignal)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           newHandler(hostStats),
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

	slog.Info("xform listening", "address", listenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve xform", "error", err)
		os.Exit(1)
	}
}

func newHandler(snapshots *hoststats.Cache) http.Handler {
	return httpapi.New(snapshots, dashboard.Handler())
}
