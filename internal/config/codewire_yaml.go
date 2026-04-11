package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// CodewireConfig represents the codewire.yaml schema.
type CodewireConfig struct {
	Preset             string                 `yaml:"preset,omitempty"`
	Image              string                 `yaml:"image,omitempty"`
	Install            string                 `yaml:"install,omitempty"`
	Startup            string                 `yaml:"startup,omitempty"`
	Secrets            *CodewireSecretsConfig `yaml:"secrets,omitempty"`
	Env                map[string]string      `yaml:"env,omitempty"`
	Ports              []PortConfig           `yaml:"ports,omitempty"`
	Mounts             []MountConfig          `yaml:"mounts,omitempty"`
	CPU                int                    `yaml:"cpu,omitempty"`
	Memory             int                    `yaml:"memory,omitempty"`
	Disk               int                    `yaml:"disk,omitempty"`
	Agents             *CodewireAgentsConfig  `yaml:"agents,omitempty"`
	Agent              string                 `yaml:"agent,omitempty"`
	InstallAgents      *bool                  `yaml:"install_agents,omitempty"`
	IncludeOrgSecrets  *bool                  `yaml:"include_org_secrets,omitempty"`
	IncludeUserSecrets *bool                  `yaml:"include_user_secrets,omitempty"`
}

type MountConfig struct {
	Source   string `yaml:"source,omitempty" toml:"source,omitempty"`
	Target   string `yaml:"target,omitempty" toml:"target,omitempty"`
	Readonly *bool  `yaml:"readonly,omitempty" toml:"readonly,omitempty"`
}

func (m *MountConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		m.Source = strings.TrimSpace(value.Value)
		m.Target = ""
		m.Readonly = nil
		if m.Source == "" {
			return fmt.Errorf("parse mount: empty value")
		}
		return nil
	case yaml.MappingNode:
		type rawMountConfig MountConfig
		var raw rawMountConfig
		if err := value.Decode(&raw); err != nil {
			return err
		}
		*m = MountConfig(raw)
		if strings.TrimSpace(m.Source) == "" {
			return fmt.Errorf("parse mount: source is required")
		}
		return nil
	default:
		return fmt.Errorf("parse mount: expected string or mapping")
	}
}

func (m MountConfig) MarshalYAML() (any, error) {
	if strings.TrimSpace(m.Source) == "" {
		return nil, fmt.Errorf("marshal mount: source is required")
	}
	if strings.TrimSpace(m.Target) == "" && m.Readonly == nil {
		return m.Source, nil
	}
	type rawMountConfig MountConfig
	return rawMountConfig(m), nil
}

func (m MountConfig) EffectiveTarget() string {
	if strings.TrimSpace(m.Target) != "" {
		return filepath.Clean(strings.TrimSpace(m.Target))
	}
	return filepath.Clean(strings.TrimSpace(m.Source))
}

func (m MountConfig) IsReadOnly() bool {
	if m.Readonly == nil {
		return true
	}
	return *m.Readonly
}

// PortConfig represents a port in codewire.yaml.
type PortConfig struct {
	Port      int    `yaml:"port,omitempty" toml:"port,omitempty"`
	HostPort  int    `yaml:"host_port,omitempty" toml:"host_port,omitempty"`
	GuestPort int    `yaml:"guest_port,omitempty" toml:"guest_port,omitempty"`
	Label     string `yaml:"label,omitempty" toml:"label,omitempty"`
}

func (p *PortConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		port, err := parsePortConfigScalar(value.Value)
		if err != nil {
			return err
		}
		*p = port
		return nil
	case yaml.MappingNode:
		type rawPortConfig struct {
			Port      int    `yaml:"port,omitempty"`
			HostPort  int    `yaml:"host_port,omitempty"`
			GuestPort int    `yaml:"guest_port,omitempty"`
			Published int    `yaml:"published,omitempty"`
			Target    int    `yaml:"target,omitempty"`
			Label     string `yaml:"label,omitempty"`
		}
		var raw rawPortConfig
		if err := value.Decode(&raw); err != nil {
			return err
		}
		port := PortConfig{
			Port:      raw.Port,
			HostPort:  raw.HostPort,
			GuestPort: raw.GuestPort,
			Label:     raw.Label,
		}
		if raw.Published > 0 {
			port.HostPort = raw.Published
		}
		if raw.Target > 0 {
			port.GuestPort = raw.Target
		}
		canonical, err := canonicalizePortConfig(port)
		if err != nil {
			return err
		}
		*p = canonical
		return nil
	default:
		return fmt.Errorf("parse port: expected scalar or mapping")
	}
}

func (p PortConfig) MarshalYAML() (any, error) {
	canonical, err := canonicalizePortConfig(p)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(canonical.Label) == "" {
		host := canonical.EffectiveHostPort()
		guest := canonical.EffectiveGuestPort()
		if host == guest {
			return guest, nil
		}
		return fmt.Sprintf("%d:%d", host, guest), nil
	}
	if canonical.Port > 0 {
		return struct {
			Port  int    `yaml:"port,omitempty"`
			Label string `yaml:"label,omitempty"`
		}{
			Port:  canonical.Port,
			Label: canonical.Label,
		}, nil
	}
	return struct {
		HostPort  int    `yaml:"host_port,omitempty"`
		GuestPort int    `yaml:"guest_port,omitempty"`
		Label     string `yaml:"label,omitempty"`
	}{
		HostPort:  canonical.HostPort,
		GuestPort: canonical.GuestPort,
		Label:     canonical.Label,
	}, nil
}

func parsePortConfigScalar(raw string) (PortConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return PortConfig{}, fmt.Errorf("parse port: empty value")
	}
	if port, err := strconv.Atoi(raw); err == nil {
		return canonicalizePortConfig(PortConfig{Port: port})
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return PortConfig{}, fmt.Errorf("parse port %q: expected PORT or HOST:PORT", raw)
	}
	hostPort, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return PortConfig{}, fmt.Errorf("parse port %q: invalid host port", raw)
	}
	guestPort, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return PortConfig{}, fmt.Errorf("parse port %q: invalid guest port", raw)
	}
	return canonicalizePortConfig(PortConfig{HostPort: hostPort, GuestPort: guestPort})
}

func canonicalizePortConfig(port PortConfig) (PortConfig, error) {
	label := strings.TrimSpace(port.Label)
	hasPort := port.Port > 0
	hasHostGuest := port.HostPort > 0 || port.GuestPort > 0
	if hasPort && hasHostGuest {
		return PortConfig{}, fmt.Errorf("parse port: use either port or host_port/guest_port")
	}
	if hasPort {
		if port.Port <= 0 {
			return PortConfig{}, fmt.Errorf("parse port: port must be greater than zero")
		}
		return PortConfig{Port: port.Port, Label: label}, nil
	}
	if port.HostPort <= 0 || port.GuestPort <= 0 {
		return PortConfig{}, fmt.Errorf("parse port: host_port and guest_port must both be greater than zero")
	}
	canonical := (PortConfig{HostPort: port.HostPort, GuestPort: port.GuestPort, Label: label}).Canonical()
	return canonical, nil
}

func (p PortConfig) EffectiveGuestPort() int {
	if p.GuestPort > 0 {
		return p.GuestPort
	}
	return p.Port
}

func (p PortConfig) EffectiveHostPort() int {
	if p.HostPort > 0 {
		return p.HostPort
	}
	return p.EffectiveGuestPort()
}

func (p PortConfig) Canonical() PortConfig {
	host := p.EffectiveHostPort()
	guest := p.EffectiveGuestPort()
	out := PortConfig{Label: p.Label}
	if host <= 0 || guest <= 0 {
		return out
	}
	if host == guest {
		out.Port = guest
		return out
	}
	out.HostPort = host
	out.GuestPort = guest
	return out
}

type CodewireAgentsConfig struct {
	Install *bool    `yaml:"install,omitempty"`
	Tools   []string `yaml:"tools,omitempty"`
}

type CodewireSecretsConfig struct {
	Org     *bool  `yaml:"org,omitempty"`
	User    *bool  `yaml:"user,omitempty"`
	Project string `yaml:"project,omitempty"`
}

func (c *CodewireAgentsConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		c.Tools = []string{strings.TrimSpace(value.Value)}
		return nil
	case yaml.SequenceNode:
		var tools []string
		if err := value.Decode(&tools); err != nil {
			return err
		}
		c.Tools = tools
		return nil
	case yaml.MappingNode:
		type rawAgentsConfig CodewireAgentsConfig
		var out rawAgentsConfig
		if err := value.Decode(&out); err != nil {
			return err
		}
		*c = CodewireAgentsConfig(out)
		return nil
	default:
		return fmt.Errorf("parse agents: expected string, list, or mapping")
	}
}

func (c *CodewireSecretsConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		c.Project = strings.TrimSpace(value.Value)
		return nil
	case yaml.MappingNode:
		type rawSecretsConfig CodewireSecretsConfig
		var out rawSecretsConfig
		if err := value.Decode(&out); err != nil {
			return err
		}
		*c = CodewireSecretsConfig(out)
		return nil
	default:
		return fmt.Errorf("parse secrets: expected string or mapping")
	}
}

func CanonicalAgentID(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "claude", "claude-code":
		return "claude-code"
	case "codex":
		return "codex"
	case "gemini", "gemini-cli":
		return "gemini-cli"
	case "aider":
		return "aider"
	default:
		return strings.TrimSpace(raw)
	}
}

func DisplayAgentID(raw string) string {
	switch CanonicalAgentID(raw) {
	case "claude-code":
		return "claude"
	case "gemini-cli":
		return "gemini"
	default:
		return CanonicalAgentID(raw)
	}
}

// LoadCodewireConfig reads and parses a codewire.yaml file.
func LoadCodewireConfig(path string) (*CodewireConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg CodewireConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return &cfg, nil
}

// WriteCodewireConfig writes a codewire.yaml file.
func WriteCodewireConfig(path string, cfg *CodewireConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
