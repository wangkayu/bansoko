package parser

import (
	"github.com/wangkayu/reltitle/configs"
	"github.com/wangkayu/reltitle/pkg/config"
)

// NewDefault returns a Parser built from the configuration embedded in the
// binary (the configs/ directory at compile time). Use New with a Config from
// config.LoadAll to run against a modified config directory instead.
func NewDefault() (*Parser, error) {
	cfg, err := config.LoadAllFS(configs.FS)
	if err != nil {
		return nil, err
	}
	return New(cfg), nil
}
