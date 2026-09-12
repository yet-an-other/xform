// Package config loads xform's runtime configuration from XFORM_* environment
// variables, falling back to the documented defaults.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yet-an-other/xform/internal/auth"
)

// Config is the Panel's runtime configuration.
type Config struct {
	ListenAddress          string // XFORM_LISTEN; Panel listen address
	AuthMode               string // XFORM_AUTH_MODE; authentication mode
	Password               string // XFORM_PASSWORD; login password, required in Password mode
	TrustedProxySecret     string // XFORM_TRUSTED_PROXY_SECRET; Admission assertion secret
	TrustedProxySignOutURL string // XFORM_TRUSTED_PROXY_SIGN_OUT_URL; optional Panel sign-out path
	XrayAPIAddress         string // XFORM_XRAY_API; xray gRPC StatsService address
	XrayConfigPath         string // XFORM_XRAY_CONFIG; xray config file for the Roster
	ConnectionsConfigPath  string // XFORM_CONNECTIONS_CONFIG; advertised connection settings, optional
	DBPath                 string // XFORM_DB; SQLite database file
	XrayUnitName           string // XFORM_XRAY_UNIT; systemd unit of the xray service
	JournalctlPath         string // XFORM_JOURNALCTL; journalctl executable
	GeoIPPath              string // XFORM_GEOIP; geoip.dat, empty searches well-known paths
}

// Load returns the documented defaults, overridden by any XFORM_* environment
// variable set to a non-empty value. Mode-specific validation happens here,
// before callers create stores, watchers, clients, or listeners.
func Load() (Config, error) {
	cfg := Config{
		ListenAddress:          env("XFORM_LISTEN", "127.0.0.1:9090"),
		AuthMode:               env("XFORM_AUTH_MODE", "password"),
		Password:               env("XFORM_PASSWORD", ""),
		TrustedProxySecret:     env("XFORM_TRUSTED_PROXY_SECRET", ""),
		TrustedProxySignOutURL: env("XFORM_TRUSTED_PROXY_SIGN_OUT_URL", ""),
		XrayAPIAddress:         env("XFORM_XRAY_API", "127.0.0.1:8080"),
		XrayConfigPath:         env("XFORM_XRAY_CONFIG", "/usr/local/etc/xray/config.json"),
		ConnectionsConfigPath:  env("XFORM_CONNECTIONS_CONFIG", ""),
		DBPath:                 env("XFORM_DB", "/var/lib/xform/xform.db"),
		XrayUnitName:           env("XFORM_XRAY_UNIT", "xray.service"),
		JournalctlPath:         env("XFORM_JOURNALCTL", "/usr/bin/journalctl"),
		GeoIPPath:              env("XFORM_GEOIP", ""),
	}
	switch cfg.AuthMode {
	case "password":
		if cfg.Password == "" {
			return Config{}, errors.New("XFORM_PASSWORD is required for password authentication (SPEC.md §7)")
		}
		if cfg.TrustedProxySecret != "" || cfg.TrustedProxySignOutURL != "" {
			return Config{}, errors.New("trusted-proxy settings are not allowed in password authentication")
		}
	case "trusted_proxy":
		if cfg.Password != "" {
			return Config{}, errors.New("XFORM_PASSWORD is not allowed in trusted proxy authentication")
		}
		if err := auth.ValidateTrustedProxySecret(cfg.TrustedProxySecret); err != nil {
			return Config{}, fmt.Errorf("XFORM_TRUSTED_PROXY_SECRET: %w", err)
		}
		if err := validateTrustedListen(cfg.ListenAddress); err != nil {
			return Config{}, err
		}
		if err := ValidateTrustedProxySignOutURL(cfg.TrustedProxySignOutURL); err != nil {
			return Config{}, fmt.Errorf("XFORM_TRUSTED_PROXY_SIGN_OUT_URL: %w", err)
		}
	default:
		return Config{}, fmt.Errorf("XFORM_AUTH_MODE %q is invalid; use password or trusted_proxy", cfg.AuthMode)
	}
	return cfg, nil
}

// ValidateTrustedProxySignOutURL accepts only a same-origin absolute path with
// an optional query. The path is navigated by the browser, so scheme-like,
// host-like, and ambiguous URL forms must be rejected at startup.
func ValidateTrustedProxySignOutURL(signOutURL string) error {
	if signOutURL == "" {
		return nil
	}
	if !utf8.ValidString(signOutURL) || !strings.HasPrefix(signOutURL, "/") || strings.HasPrefix(signOutURL, "//") || strings.ContainsAny(signOutURL, "\\#") {
		return errors.New("trusted proxy sign-out URL must be a same-origin absolute path")
	}
	for _, character := range signOutURL {
		if unicode.IsControl(character) {
			return errors.New("trusted proxy sign-out URL must not contain control characters")
		}
	}
	parsed, err := url.Parse(signOutURL)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.User != nil || !strings.HasPrefix(parsed.Path, "/") {
		return errors.New("trusted proxy sign-out URL must be a same-origin absolute path")
	}
	if _, err := url.PathUnescape(parsed.RawQuery); err != nil {
		return errors.New("trusted proxy sign-out URL contains an invalid query escape")
	}
	return nil
}

func validateTrustedListen(address string) error {
	if strings.HasPrefix(address, "unix:") {
		path := strings.TrimPrefix(address, "unix:")
		if path == "" || !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
			return errors.New("XFORM_LISTEN in trusted proxy mode must be unix:/absolute/path or a loopback TCP address")
		}
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("XFORM_LISTEN in trusted proxy mode must be unix:/absolute/path or a loopback TCP address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("XFORM_LISTEN in trusted proxy mode must be unix:/absolute/path or a loopback TCP address")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
