// Package config loads xform's runtime configuration from XFORM_* environment
// variables, falling back to the documented defaults.
package config

import (
	"errors"
	"fmt"
	"os"
)

// Config is the Panel's runtime configuration.
type Config struct {
	ListenAddress         string // XFORM_LISTEN; Panel listen address
	AuthMode              string // XFORM_AUTH_MODE; authentication mode
	Password              string // XFORM_PASSWORD; login password, required in Password mode
	XrayAPIAddress        string // XFORM_XRAY_API; xray gRPC StatsService address
	XrayConfigPath        string // XFORM_XRAY_CONFIG; xray config file for the Roster
	ConnectionsConfigPath string // XFORM_CONNECTIONS_CONFIG; advertised connection settings, optional
	DBPath                string // XFORM_DB; SQLite database file
	XrayUnitName          string // XFORM_XRAY_UNIT; systemd unit of the xray service
	JournalctlPath        string // XFORM_JOURNALCTL; journalctl executable
	GeoIPPath             string // XFORM_GEOIP; geoip.dat, empty searches well-known paths
}

// Load returns the documented defaults, overridden by any XFORM_* environment
// variable set to a non-empty value. Password authentication is the only
// currently implemented mode and requires XFORM_PASSWORD; later modes add
// their own discriminated settings behind the same seam.
func Load() (Config, error) {
	cfg := Config{
		ListenAddress:         env("XFORM_LISTEN", "127.0.0.1:9090"),
		AuthMode:              env("XFORM_AUTH_MODE", "password"),
		Password:              env("XFORM_PASSWORD", ""),
		XrayAPIAddress:        env("XFORM_XRAY_API", "127.0.0.1:8080"),
		XrayConfigPath:        env("XFORM_XRAY_CONFIG", "/usr/local/etc/xray/config.json"),
		ConnectionsConfigPath: env("XFORM_CONNECTIONS_CONFIG", ""),
		DBPath:                env("XFORM_DB", "/var/lib/xform/xform.db"),
		XrayUnitName:          env("XFORM_XRAY_UNIT", "xray.service"),
		JournalctlPath:        env("XFORM_JOURNALCTL", "/usr/bin/journalctl"),
		GeoIPPath:             env("XFORM_GEOIP", ""),
	}
	if cfg.AuthMode != "password" {
		return Config{}, fmt.Errorf("XFORM_AUTH_MODE %q is not supported; use password", cfg.AuthMode)
	}
	if cfg.Password == "" {
		return Config{}, errors.New("XFORM_PASSWORD is required for password authentication (SPEC.md §7)")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
