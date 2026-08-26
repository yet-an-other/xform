package config_test

import (
	"os"
	"testing"

	"github.com/yet-an-other/xform/internal/config"
)

// clearEnv unsets every XFORM_* variable so tests are hermetic regardless of
// the surrounding shell.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"XFORM_LISTEN",
		"XFORM_PASSWORD",
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
