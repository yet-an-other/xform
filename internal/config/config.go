// Package config loads xform's runtime configuration from XFORM_* environment
// variables, falling back to the SPEC.md §7 defaults.
package config

import "os"

// Config is the panel's runtime configuration. Password is a placeholder until
// login lands (SPEC.md §7 requires it once auth exists); the xray settings are
// consumed by later slices (status, collector).
type Config struct {
	ListenAddress  string // XFORM_LISTEN — panel listen address
	Password       string // XFORM_PASSWORD — login password (placeholder)
	XrayAPIAddress string // XFORM_XRAY_API — xray gRPC StatsService address
	XrayConfigPath string // XFORM_XRAY_CONFIG — xray config file (user roster)
	DBPath         string // XFORM_DB — SQLite database file
	XrayUnitName   string // XFORM_XRAY_UNIT — systemd unit of the xray service
}

// Load returns the SPEC.md §7 defaults, overridden by any XFORM_* environment
// variable that is set to a non-empty value.
func Load() Config {
	return Config{
		ListenAddress:  env("XFORM_LISTEN", "127.0.0.1:9090"),
		Password:       env("XFORM_PASSWORD", ""),
		XrayAPIAddress: env("XFORM_XRAY_API", "127.0.0.1:8080"),
		XrayConfigPath: env("XFORM_XRAY_CONFIG", "/usr/local/etc/xray/config.json"),
		DBPath:         env("XFORM_DB", "/var/lib/xform/xform.db"),
		XrayUnitName:   env("XFORM_XRAY_UNIT", "xray.service"),
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
