package agent

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"mining_shield/internal/config"
)

// ServerEntry 一个可用的隧道服务器（按顺序轮换 failover）。
type ServerEntry struct {
	URL   string `yaml:"url"`   // wss://your-domain.com/<secret-path>
	Token string `yaml:"token"` // 与服务器端配置的 token 一致
}

// Listener 一个矿机接入监听端口，绑定一个路由名。
// 同一路由的矿机流量会被服务器端转发到该路由对应的矿池组。
type Listener struct {
	Listen string `yaml:"listen"` // 如 "0.0.0.0:3333"
	Route  string `yaml:"route"`  // 路由名，空 = 默认路由（对应服务器端 pools）
}

type Config struct {
	// Listen 单端口简写（默认路由）。与 Listeners 二选一或叠加。
	Listen string `yaml:"listen"`
	// Listeners 多币种监听列表，每项绑定一个路由
	Listeners []Listener `yaml:"listeners"`
	// Servers 隧道服务器列表，至少一个
	Servers []ServerEntry `yaml:"servers"`

	PingInterval config.Duration `yaml:"ping_interval"` // 隧道心跳间隔，默认 25s
	ReadTimeout  config.Duration `yaml:"read_timeout"`  // 隧道读超时，默认 90s
	MinBackoff   config.Duration `yaml:"min_backoff"`   // 重连最小退避，默认 1s
	MaxBackoff   config.Duration `yaml:"max_backoff"`   // 重连最大退避，默认 30s
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{
		PingInterval: config.Duration(25 * time.Second),
		ReadTimeout:  config.Duration(90 * time.Second),
		MinBackoff:   config.Duration(1 * time.Second),
		MaxBackoff:   config.Duration(30 * time.Second),
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen != "" {
		cfg.Listeners = append([]Listener{{Listen: cfg.Listen}}, cfg.Listeners...)
	}
	if len(cfg.Listeners) == 0 {
		return nil, errors.New("config: at least one listener (listen or listeners) is required")
	}
	seen := map[string]bool{}
	for i, l := range cfg.Listeners {
		if l.Listen == "" {
			return nil, fmt.Errorf("config: listeners[%d].listen is required", i)
		}
		if len(l.Route) > 64 {
			return nil, fmt.Errorf("config: listeners[%d].route too long (max 64 chars)", i)
		}
		if seen[l.Listen] {
			return nil, fmt.Errorf("config: duplicate listen address %q", l.Listen)
		}
		seen[l.Listen] = true
	}
	if len(cfg.Servers) == 0 {
		return nil, errors.New("config: at least one server is required")
	}
	for i, s := range cfg.Servers {
		if s.URL == "" || s.Token == "" {
			return nil, fmt.Errorf("config: servers[%d] requires url and token", i)
		}
	}
	return cfg, nil
}
