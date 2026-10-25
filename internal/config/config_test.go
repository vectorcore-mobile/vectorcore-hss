package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDSNSqliteAddsBusyTimeoutAndWAL(t *testing.T) {
	d := DatabaseConfig{Type: "sqlite", Database: "/opt/vectorcore/hss.db"}
	dsn, err := d.DSN()
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	if !strings.Contains(dsn, "_busy_timeout=5000") {
		t.Errorf("DSN() = %q, want it to contain _busy_timeout=5000", dsn)
	}
	if !strings.Contains(dsn, "_journal_mode=WAL") {
		t.Errorf("DSN() = %q, want it to contain _journal_mode=WAL", dsn)
	}
	if !strings.Contains(dsn, "_txlock=immediate") {
		t.Errorf("DSN() = %q, want it to contain _txlock=immediate", dsn)
	}
}

func TestDSNSqliteAppendsToExistingQueryParams(t *testing.T) {
	d := DatabaseConfig{Type: "sqlite", Database: "file:test?mode=memory&cache=shared"}
	dsn, err := d.DSN()
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	if !strings.Contains(dsn, "mode=memory") || !strings.Contains(dsn, "cache=shared") {
		t.Errorf("DSN() = %q, want original query params preserved", dsn)
	}
	if !strings.Contains(dsn, "_busy_timeout=5000") || !strings.Contains(dsn, "_journal_mode=WAL") || !strings.Contains(dsn, "_txlock=immediate") {
		t.Errorf("DSN() = %q, want busy_timeout/journal_mode/txlock appended", dsn)
	}
	if strings.Count(dsn, "?") != 1 {
		t.Errorf("DSN() = %q, want exactly one '?' (rest joined with '&')", dsn)
	}
}

func TestDSNSqliteRespectsExplicitPragmas(t *testing.T) {
	d := DatabaseConfig{Type: "sqlite", Database: "file:test?_busy_timeout=1000"}
	dsn, err := d.DSN()
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	if dsn != d.Database {
		t.Errorf("DSN() = %q, want it left untouched when the operator already set a pragma, got %q", dsn, d.Database)
	}
}

func TestDSNPostgresIncludesConnectTimeout(t *testing.T) {
	d := DatabaseConfig{Type: "postgresql", Host: "db", Port: 5432, Username: "hss", Password: "hss", Database: "hss"}
	dsn, err := d.DSN()
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	if !strings.Contains(dsn, "connect_timeout=5") {
		t.Errorf("DSN() = %q, want default connect_timeout=5", dsn)
	}

	d.ConnectTimeout = 10
	dsn, err = d.DSN()
	if err != nil {
		t.Fatalf("DSN() error: %v", err)
	}
	if !strings.Contains(dsn, "connect_timeout=10") {
		t.Errorf("DSN() = %q, want configured connect_timeout=10", dsn)
	}
}

func TestLoadNormalizesFiveGCConfig(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
5gc:
  sbi:
    bind_address: "::"
    advertise_address: "10.0.2.117"
    bind_port: 7777
    oauth2_enabled: true
    oauth2_bypass: false
    client:
      mode: scp
      scp_address: "http://127.0.0.200:7777/nscp-proxy/v1"
      reconnect_holdoff_time: 5s
  udm:
    enabled: true
    mcc: "311"
    mnc: "435"
    nrf_address: "http://127.0.0.10:7777"
    nf_instance_id: "udm-id"
    suci_decryption_keys:
      - key_id: 1
        scheme: 1
        key_file: /tmp/key.pem
  pcf:
    enabled: true
    mcc: "311"
    mnc: "435"
    nrf_address: "http://127.0.0.10:7777"
    nf_instance_id: "pcf-id"
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.UDM.Enabled || !cfg.PCF.Enabled {
		t.Fatalf("expected UDM and PCF enabled, got %+v %+v", cfg.UDM, cfg.PCF)
	}
	if cfg.UDM.AdvertiseAddress != "10.0.2.117" || cfg.PCF.AdvertiseAddress != "10.0.2.117" {
		t.Fatalf("expected shared advertise address, got %q %q", cfg.UDM.AdvertiseAddress, cfg.PCF.AdvertiseAddress)
	}
	if cfg.UDM.BindPort != 7777 || cfg.PCF.BindPort != 7777 {
		t.Fatalf("expected shared bind port 7777, got %d %d", cfg.UDM.BindPort, cfg.PCF.BindPort)
	}
	if !cfg.UDM.OAuth2Enabled || cfg.UDM.OAuth2Bypass {
		t.Fatalf("expected shared OAuth settings on UDM, got %+v", cfg.UDM)
	}
	if cfg.PCF.SBIClient.Mode != "scp" {
		t.Fatalf("expected shared SBI client mode on PCF, got %+v", cfg.PCF.SBIClient)
	}
	if cfg.UDM.SBIClient.ReconnectHoldoffTime != 5*time.Second || cfg.PCF.SBIClient.ReconnectHoldoffTime != 5*time.Second {
		t.Fatalf("expected shared reconnect holdoff on UDM/PCF, got %v %v", cfg.UDM.SBIClient.ReconnectHoldoffTime, cfg.PCF.SBIClient.ReconnectHoldoffTime)
	}
	if len(cfg.UDM.SUCIDecryptionKeys) != 1 {
		t.Fatalf("expected SUCI decryption keys to normalize, got %+v", cfg.UDM.SUCIDecryptionKeys)
	}
}

func TestLoadDefaultsReconnectHoldoff(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UDM.SBIClient.ReconnectHoldoffTime != 2*time.Second {
		t.Fatalf("expected default UDM reconnect holdoff 2s, got %v", cfg.UDM.SBIClient.ReconnectHoldoffTime)
	}
	if cfg.PCF.SBIClient.ReconnectHoldoffTime != 2*time.Second {
		t.Fatalf("expected default PCF reconnect holdoff 2s, got %v", cfg.PCF.SBIClient.ReconnectHoldoffTime)
	}
}

func TestLoadDefaultsPCRFTFTHandlingWhenMissing(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PCRF.TFTHandling != "standard" {
		t.Fatalf("expected standard TFT handling, got %q", cfg.PCRF.TFTHandling)
	}
}

func TestLoadDefaultsPCRFTFTHandlingWhenEmpty(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
pcrf:
  tft_handling: ""
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PCRF.TFTHandling != "standard" {
		t.Fatalf("expected standard TFT handling, got %q", cfg.PCRF.TFTHandling)
	}
}

func TestLoadDefaultsPCRFTFTHandlingWhenNull(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
pcrf:
  tft_handling: null
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PCRF.TFTHandling != "standard" {
		t.Fatalf("expected standard TFT handling, got %q", cfg.PCRF.TFTHandling)
	}
}

func TestLoadInvalidPCRFTFTHandlingLogsWarningAndDefaults(t *testing.T) {
	var buf bytes.Buffer
	oldOutput := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
	}()

	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
pcrf:
  tft_handling: broken
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PCRF.TFTHandling != "standard" {
		t.Fatalf("expected standard TFT handling, got %q", cfg.PCRF.TFTHandling)
	}
	if !strings.Contains(buf.String(), `Invalid pcrf.tft_handling value "broken"; defaulting to standard`) {
		t.Fatalf("expected invalid config warning, got %q", buf.String())
	}
}

func TestLoadKeepsLegacyHNetKeys(t *testing.T) {
	cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
database:
  db_type: sqlite
  database: ":memory:"
udm:
  enabled: true
  hnet_keys:
    - key_id: 1
      scheme: 1
      key_file: /tmp/key.pem
`
	path := writeTestConfig(t, cfgText)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.UDM.SUCIDecryptionKeys) != 1 {
		t.Fatalf("expected legacy hnet_keys to map, got %+v", cfg.UDM.SUCIDecryptionKeys)
	}
}

func TestLoadValidatesDiameterDSCP(t *testing.T) {
	tests := []struct {
		name    string
		dscp    int
		wantErr bool
	}{
		{name: "default", dscp: 0},
		{name: "ef", dscp: 46},
		{name: "max", dscp: 63},
		{name: "negative", dscp: -1, wantErr: true},
		{name: "too high", dscp: 64, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgText := `
hss:
  OriginHost: hss01.example.org
  OriginRealm: example.org
  DiameterDSCP: ` + strconv.Itoa(tt.dscp) + `
database:
  db_type: sqlite
  database: ":memory:"
`
			path := writeTestConfig(t, cfgText)
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Fatal("expected Load() error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
