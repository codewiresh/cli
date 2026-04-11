package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCodewireConfigParsesComposeStylePorts(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "codewire.yaml")
	data := `ports:
  - 3000
  - "18080:8080"
  - port: 4000
    label: web
  - published: 15432
    target: 5432
    label: postgres
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := LoadCodewireConfig(path)
	if err != nil {
		t.Fatalf("LoadCodewireConfig() error = %v", err)
	}

	want := []PortConfig{
		{Port: 3000},
		{HostPort: 18080, GuestPort: 8080},
		{Port: 4000, Label: "web"},
		{HostPort: 15432, GuestPort: 5432, Label: "postgres"},
	}
	if !reflect.DeepEqual(cfg.Ports, want) {
		t.Fatalf("cfg.Ports = %#v, want %#v", cfg.Ports, want)
	}
}

func TestWriteCodewireConfigUsesComposeStylePortsWhenPossible(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "codewire.yaml")
	cfg := &CodewireConfig{
		Ports: []PortConfig{
			{Port: 3000},
			{HostPort: 18080, GuestPort: 8080},
			{Port: 4000, Label: "web"},
			{HostPort: 15432, GuestPort: 5432, Label: "postgres"},
		},
	}

	if err := WriteCodewireConfig(path, cfg); err != nil {
		t.Fatalf("WriteCodewireConfig() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "- 3000\n") {
		t.Fatalf("expected scalar same-port syntax, got %q", got)
	}
	if !strings.Contains(got, "18080:8080") {
		t.Fatalf("expected host:guest short syntax, got %q", got)
	}
	if !strings.Contains(got, "port: 4000") {
		t.Fatalf("expected labeled same-port mapping, got %q", got)
	}
	if !strings.Contains(got, "host_port: 15432") || !strings.Contains(got, "guest_port: 5432") {
		t.Fatalf("expected labeled host/guest mapping, got %q", got)
	}

	reloaded, err := LoadCodewireConfig(path)
	if err != nil {
		t.Fatalf("LoadCodewireConfig() reload error = %v", err)
	}
	if !reflect.DeepEqual(reloaded.Ports, cfg.Ports) {
		t.Fatalf("reloaded.Ports = %#v, want %#v", reloaded.Ports, cfg.Ports)
	}
}

func TestLoadCodewireConfigRejectsPartialPortMappings(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "codewire.yaml")
	data := `ports:
  - host_port: 18080
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadCodewireConfig(path)
	if err == nil {
		t.Fatal("expected LoadCodewireConfig() to fail")
	}
	if !strings.Contains(err.Error(), "host_port and guest_port must both be greater than zero") {
		t.Fatalf("error = %q, want partial port mapping error", err)
	}
}
