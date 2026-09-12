package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/config"
)

// clearEnv unsets every XFORM_* variable so tests are hermetic regardless of
// the surrounding shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"XFORM_LISTEN",
		"XFORM_AUTH_MODE",
		"XFORM_PASSWORD",
		"XFORM_TRUSTED_PROXY_SECRET",
		"XFORM_XRAY_API",
		"XFORM_XRAY_CONFIG",
		"XFORM_CONNECTIONS_CONFIG",
		"XFORM_DB",
		"XFORM_XRAY_UNIT",
		"XFORM_JOURNALCTL",
		"XFORM_GEOIP",
	} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("XFORM_PASSWORD", "test-password")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:9090" {
		t.Errorf("ListenAddress = %q, want %q", cfg.ListenAddress, "127.0.0.1:9090")
	}
	if cfg.AuthMode != "password" {
		t.Errorf("AuthMode = %q, want password", cfg.AuthMode)
	}
	if cfg.XrayAPIAddress != "127.0.0.1:8080" {
		t.Errorf("XrayAPIAddress = %q, want %q", cfg.XrayAPIAddress, "127.0.0.1:8080")
	}
	if cfg.XrayConfigPath != "/usr/local/etc/xray/config.json" {
		t.Errorf("XrayConfigPath = %q, want %q", cfg.XrayConfigPath, "/usr/local/etc/xray/config.json")
	}
	if cfg.ConnectionsConfigPath != "" {
		t.Errorf("ConnectionsConfigPath = %q, want no default", cfg.ConnectionsConfigPath)
	}
	if cfg.DBPath != "/var/lib/xform/xform.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/var/lib/xform/xform.db")
	}
	if cfg.XrayUnitName != "xray.service" {
		t.Errorf("XrayUnitName = %q, want %q", cfg.XrayUnitName, "xray.service")
	}
	if cfg.JournalctlPath != "/usr/bin/journalctl" {
		t.Errorf("JournalctlPath = %q, want %q", cfg.JournalctlPath, "/usr/bin/journalctl")
	}
	if cfg.Password != "test-password" {
		t.Errorf("Password = %q, want the XFORM_PASSWORD value", cfg.Password)
	}
}

func TestLoadReadsEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("XFORM_LISTEN", "0.0.0.0:8080")
	t.Setenv("XFORM_AUTH_MODE", "password")
	t.Setenv("XFORM_PASSWORD", "s3cret")
	t.Setenv("XFORM_XRAY_API", "127.0.0.1:10086")
	t.Setenv("XFORM_XRAY_CONFIG", "/etc/xray/config.json")
	t.Setenv("XFORM_CONNECTIONS_CONFIG", "/etc/xform/connections.json")
	t.Setenv("XFORM_DB", "/srv/xform/panel.db")
	t.Setenv("XFORM_XRAY_UNIT", "xray-vless.service")
	t.Setenv("XFORM_JOURNALCTL", "/bin/journalctl")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.ListenAddress != "0.0.0.0:8080" {
		t.Errorf("ListenAddress = %q, want the XFORM_LISTEN override", cfg.ListenAddress)
	}
	if cfg.AuthMode != "password" {
		t.Errorf("AuthMode = %q, want password", cfg.AuthMode)
	}
	if cfg.Password != "s3cret" {
		t.Errorf("Password = %q, want the XFORM_PASSWORD override", cfg.Password)
	}
	if cfg.XrayAPIAddress != "127.0.0.1:10086" {
		t.Errorf("XrayAPIAddress = %q, want the XFORM_XRAY_API override", cfg.XrayAPIAddress)
	}
	if cfg.XrayConfigPath != "/etc/xray/config.json" {
		t.Errorf("XrayConfigPath = %q, want the XFORM_XRAY_CONFIG override", cfg.XrayConfigPath)
	}
	if cfg.ConnectionsConfigPath != "/etc/xform/connections.json" {
		t.Errorf("ConnectionsConfigPath = %q, want the XFORM_CONNECTIONS_CONFIG override", cfg.ConnectionsConfigPath)
	}
	if cfg.DBPath != "/srv/xform/panel.db" {
		t.Errorf("DBPath = %q, want the XFORM_DB override", cfg.DBPath)
	}
	if cfg.XrayUnitName != "xray-vless.service" {
		t.Errorf("XrayUnitName = %q, want the XFORM_XRAY_UNIT override", cfg.XrayUnitName)
	}
	if cfg.JournalctlPath != "/bin/journalctl" {
		t.Errorf("JournalctlPath = %q, want the XFORM_JOURNALCTL override", cfg.JournalctlPath)
	}
}

func TestLoadRequiresPassword(t *testing.T) {
	clearEnv(t)

	if _, err := config.Load(); err == nil {
		t.Fatal("load succeeded without XFORM_PASSWORD, want an error (SPEC.md §7 requires it)")
	}
}

func TestLoadTrustedProxyMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("XFORM_AUTH_MODE", "trusted_proxy")
	t.Setenv("XFORM_TRUSTED_PROXY_SECRET", strings.Repeat("ab", 32))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load trusted proxy config: %v", err)
	}
	if cfg.AuthMode != "trusted_proxy" {
		t.Errorf("AuthMode = %q, want trusted_proxy", cfg.AuthMode)
	}
	if cfg.Password != "" {
		t.Errorf("Password = %q, want empty in trusted proxy mode", cfg.Password)
	}
	if cfg.TrustedProxySecret != strings.Repeat("ab", 32) {
		t.Error("trusted proxy secret was not loaded")
	}
}

func TestLoadRejectsUnsafeTrustedProxyConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		listen   string
		password string
	}{
		{name: "missing secret", listen: "127.0.0.1:9090"},
		{name: "short secret", secret: "ab", listen: "127.0.0.1:9090"},
		{name: "uppercase secret", secret: strings.Repeat("AB", 32), listen: "127.0.0.1:9090"},
		{name: "non hexadecimal secret", secret: strings.Repeat("ag", 32), listen: "127.0.0.1:9090"},
		{name: "password also configured", secret: strings.Repeat("ab", 32), password: "password", listen: "127.0.0.1:9090"},
		{name: "wildcard listener", secret: strings.Repeat("ab", 32), listen: "0.0.0.0:9090"},
		{name: "hostname listener", secret: strings.Repeat("ab", 32), listen: "localhost:9090"},
		{name: "non loopback listener", secret: strings.Repeat("ab", 32), listen: "192.0.2.10:9090"},
		{name: "missing TCP port", secret: strings.Repeat("ab", 32), listen: "127.0.0.1"},
		{name: "relative Unix path", secret: strings.Repeat("ab", 32), listen: "unix:xform.sock"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("XFORM_AUTH_MODE", "trusted_proxy")
			t.Setenv("XFORM_TRUSTED_PROXY_SECRET", test.secret)
			t.Setenv("XFORM_LISTEN", test.listen)
			if test.password != "" {
				t.Setenv("XFORM_PASSWORD", test.password)
			}

			_, err := config.Load()
			if err == nil {
				t.Fatal("load accepted unsafe trusted proxy configuration")
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("configuration error contains the Admission secret: %q", err)
			}
		})
	}
}

func TestLoadRejectsTrustedSecretInPasswordMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("XFORM_PASSWORD", "password")
	t.Setenv("XFORM_TRUSTED_PROXY_SECRET", strings.Repeat("ab", 32))

	if _, err := config.Load(); err == nil {
		t.Fatal("load accepted trusted proxy settings in password mode")
	}
}

func TestLoadAcceptsTrustedUnixAndIPv6LoopbackListeners(t *testing.T) {
	for _, listen := range []string{"unix:/run/xform/xform.sock", "[::1]:9090"} {
		t.Run(listen, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("XFORM_AUTH_MODE", "trusted_proxy")
			t.Setenv("XFORM_TRUSTED_PROXY_SECRET", strings.Repeat("ab", 32))
			t.Setenv("XFORM_LISTEN", listen)
			if _, err := config.Load(); err != nil {
				t.Fatalf("load %q: %v", listen, err)
			}
		})
	}
}

func TestLoadRejectsUnknownAuthenticationMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("XFORM_AUTH_MODE", "something_else")

	if _, err := config.Load(); err == nil {
		t.Fatal("load accepted an unknown authentication mode")
	}
}
