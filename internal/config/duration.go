// Package config 提供两端共用的配置辅助类型。
package config

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 支持在 YAML 中写 "10s"、"1m30s" 这类字符串。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }
