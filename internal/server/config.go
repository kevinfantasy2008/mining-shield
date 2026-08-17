package server

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"mining_shield/internal/config"
)

type Config struct {
	// Listen 只监听本机回环，由 Nginx 在 443 反代进来，如 "127.0.0.1:8080"
	Listen string `yaml:"listen"`
	// Path WebSocket 秘密路径，需带前导斜杠，如 "/a1b2c3d4-secret"
	Path string `yaml:"path"`
	// Token 认证令牌（长随机串），本地端在 X-Auth-Token 头携带
	Token string `yaml:"token"`
	// Pools 真实矿池地址列表，按顺序 failover。
	// 支持 stratum+tcp://host:port 与 stratum+ssl://host:port
	Pools []string `yaml:"pools"`

	DialTimeout config.Duration `yaml:"dial_timeout"` // 拨号矿池超时，默认 10s
	ReadTimeout config.Duration `yaml:"read_timeout"` // 隧道读超时，默认 90s
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{
		DialTimeout: config.Duration(10 * time.Second),
		ReadTimeout: config.Duration(90 * time.Second),
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" {
		return nil, errors.New("config: listen is required")
	}
	if cfg.Path == "" || cfg.Path[0] != '/' {
		return nil, errors.New("config: path is required and must start with /")
	}
	if len(cfg.Token) < 16 {
		return nil, errors.New("config: token must be at least 16 chars (use a long random string)")
	}
	if len(cfg.Pools) == 0 {
		return nil, errors.New("config: at least one pool is required")
	}
	for _, p := range cfg.Pools {
		if _, _, err := parsePoolURL(p); err != nil {
			return nil, fmt.Errorf("config: pool %q: %w", p, err)
		}
	}
	return cfg, nil
}

// parsePoolURL 返回 (scheme, host:port)。scheme 为 "tcp" 或 "ssl"。
func parsePoolURL(raw string) (scheme, addr string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	switch u.Scheme {
	case "stratum+tcp":
		scheme = "tcp"
	case "stratum+ssl":
		scheme = "ssl"
	default:
		return "", "", fmt.Errorf("unsupported scheme %q (want stratum+tcp or stratum+ssl)", u.Scheme)
	}
	if u.Host == "" {
		return "", "", errors.New("missing host:port")
	}
	return scheme, u.Host, nil
}
