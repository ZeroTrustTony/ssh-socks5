package config

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

type Config struct {
	SSH          SSHConfig        `yaml:"ssh"`
	SOCKS5       SOCKS5Config     `yaml:"socks5"`
	UDP          UDPConfig        `yaml:"udp"`
	StartupTest  StartupTestConfig `yaml:"startup_test"`
	IdleTimeout  Duration         `yaml:"idle_timeout"`
	MaxClients   int              `yaml:"max_clients"`
	LogLevel     string           `yaml:"log_level"`
}

type SSHConfig struct {
	Host    string        `yaml:"host"`
	Port    int           `yaml:"port"`
	User    string        `yaml:"user"`
	Auth    SSHAuth       `yaml:"auth"`
	HostKey HostKeyConfig `yaml:"host_key"`
}

type SSHAuth struct {
	Method         string `yaml:"method"`
	PrivateKeyPath string `yaml:"private_key_path"`
	Password       string `yaml:"password"`
}

type HostKeyConfig struct {
	Verify bool   `yaml:"verify"`
	Key    string `yaml:"key"`
}

type SOCKS5Config struct {
	Listen   string `yaml:"listen"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type UDPConfig struct {
	Enabled    bool   `yaml:"enabled"`
	RemotePath string `yaml:"remote_path"`
	Port       int    `yaml:"port"`
	AuthKey    string `yaml:"auth_key"`
}

type StartupTestConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{
		SSH: SSHConfig{
			Port: 22,
		},
		UDP: UDPConfig{
			Port: 38473,
		},
		StartupTest: StartupTestConfig{
			URL: "https://www.google.com",
		},
		IdleTimeout: Duration{Duration: 5 * time.Minute},
		LogLevel:    "info",
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c.SSH.Host == "" {
		return fmt.Errorf("ssh.host is required")
	}
	if c.SSH.User == "" {
		return fmt.Errorf("ssh.user is required")
	}
	if c.SSH.Port <= 0 || c.SSH.Port > 65535 {
		return fmt.Errorf("ssh.port must be between 1 and 65535")
	}

	switch c.SSH.Auth.Method {
	case "key", "private_key":
		if c.SSH.Auth.PrivateKeyPath == "" {
			return fmt.Errorf("ssh.auth.private_key_path is required for key auth")
		}
	case "password":
		if c.SSH.Auth.Password == "" {
			return fmt.Errorf("ssh.auth.password is required for password auth")
		}
	default:
		return fmt.Errorf("ssh.auth.method must be 'key' or 'password'")
	}

	if c.SSH.HostKey.Verify {
		if strings.TrimSpace(c.SSH.HostKey.Key) == "" {
			return fmt.Errorf("ssh.host_key.key is required when ssh.host_key.verify is true")
		}
		if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.SSH.HostKey.Key)); err != nil {
			return fmt.Errorf("ssh.host_key.key is not a valid SSH public key: %w", err)
		}
	}

	if c.SOCKS5.Listen == "" {
		return fmt.Errorf("socks5.listen is required")
	}
	if c.SOCKS5.Username == "" {
		return fmt.Errorf("socks5.username is required")
	}
	if c.SOCKS5.Password == "" {
		return fmt.Errorf("socks5.password is required")
	}
	if c.IdleTimeout.Duration <= 0 {
		return fmt.Errorf("idle_timeout must be positive")
	}

	switch c.LogLevel {
	case "debug", "info", "error":
	default:
		return fmt.Errorf("log_level must be debug, info, or error")
	}

	if c.UDP.Enabled {
		if c.UDP.RemotePath == "" {
			return fmt.Errorf("udp.remote_path is required when udp.enabled is true")
		}
		if err := validateRemoteExecPath(c.UDP.RemotePath); err != nil {
			return fmt.Errorf("udp.remote_path: %w", err)
		}
		if c.UDP.AuthKey == "" {
			return fmt.Errorf("udp.auth_key is required when udp.enabled is true")
		}
		if strings.ContainsAny(c.UDP.AuthKey, "\x00\n\r") {
			return fmt.Errorf("udp.auth_key must not contain null or newline characters")
		}
		if c.UDP.Port <= 0 || c.UDP.Port > 65535 {
			return fmt.Errorf("udp.port must be between 1 and 65535")
		}
	}

	if c.StartupTest.Enabled {
		if strings.TrimSpace(c.StartupTest.URL) == "" {
			return fmt.Errorf("startup_test.url is required when startup_test.enabled is true")
		}
	}

	return nil
}

// HealthCheckAddr returns a loopback address suitable for probing the SOCKS5 listener.
func (c *Config) HealthCheckAddr() (string, error) {
	host, port, err := net.SplitHostPort(c.SOCKS5.Listen)
	if err != nil {
		return "", fmt.Errorf("parse socks5.listen: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// SOCKS5ProxyURL returns a curl-compatible SOCKS5 proxy URL.
func (c *Config) SOCKS5ProxyURL() (string, error) {
	addr, err := c.HealthCheckAddr()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("socks5://%s:%s@%s", c.SOCKS5.Username, c.SOCKS5.Password, addr), nil
}

func (c *SSHConfig) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// validateRemoteExecPath rejects paths that could break out of a remote shell command.
func validateRemoteExecPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("must be an absolute path")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("must not contain '..'")
	}
	if strings.ContainsAny(path, " \t\n\r\"'`$&|;<>(){}[]!*?#~\x00") {
		return fmt.Errorf("contains unsafe characters")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("contains control characters")
		}
	}
	return nil
}
