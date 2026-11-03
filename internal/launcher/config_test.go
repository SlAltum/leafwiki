package launcher

import (
	"os"
	"path/filepath"
	"testing"
)

func keepAll(string) bool { return true }

func TestLoad_MissingFileIsAFirstStart(t *testing.T) {
	cfg, err := Load(t.TempDir(), keepAll)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for a missing file", err)
	}
	if cfg.Version != configVersion || cfg.AutoRefresh || len(cfg.Params) != 0 {
		t.Fatalf("Load() = %+v, want the defaults", cfg)
	}
}

func TestLoad_DropsUnknownAndExcludedParams(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "version": 7,
  "params": {"port": "8081", "no-such-flag": "x", "jwt-secret": "leak"},
  "auto_refresh": true,
  "unknown_field": 1
}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// keep stands in for "the CLI's flag names minus the excluded ones": only
	// port is a known, persistable flag here.
	keep := func(name string) bool { return name == "port" }
	cfg, err := Load(dir, keep)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Params; len(got) != 1 || got["port"] != "8081" {
		t.Fatalf("Params = %v, want only port", got)
	}
	if cfg.Version != configVersion {
		t.Fatalf("Version = %d, want %d", cfg.Version, configVersion)
	}
	if !cfg.AutoRefresh {
		t.Fatal("AutoRefresh = false, want true")
	}
}

func TestLoad_CorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(dir, keepAll); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}

func TestSave_RoundTripsAndSkipsUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	keep := func(name string) bool { return name != "jwt-secret" }

	cfg := DefaultConfig()
	cfg.Params["port"] = "8081"
	cfg.Params["jwt-secret"] = "leak"
	cfg.AutoRefresh = true

	if err := cfg.Save(dir, keep); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path := filepath.Join(dir, configFileName)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}

	loaded, err := Load(dir, keep)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Params) != 1 || loaded.Params["port"] != "8081" || !loaded.AutoRefresh {
		t.Fatalf("Load() after Save() = %+v, want only port and auto_refresh", loaded)
	}

	if err := loaded.Save(dir, keep); err != nil {
		t.Fatalf("Save() twice error = %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Save() rewrote an unchanged file")
	}
}
