// Package cli 实现 wxctl 命令行：在本地或服务器上调用 wx-bot 服务。
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	ModeLocal  = "local"
	ModeServer = "server"

	defaultLocalURL  = "https://wx1.cookup.cn"
	defaultServerURL = "http://127.0.0.1:1240"
)

// Config 配置文件内容。优先级：命令行参数 > 环境变量 > 配置文件 > 默认值
type Config struct {
	Mode     string   `yaml:"mode,omitempty"`      // local（默认）或 server
	URL      string   `yaml:"url,omitempty"`       // wx-bot 服务地址
	Token    string   `yaml:"token,omitempty"`     // 访问 token
	Skin     string   `yaml:"skin,omitempty"`      // 默认皮肤
	CacheDir string   `yaml:"cache_dir,omitempty"` // 图片上传缓存目录
	Defaults Defaults `yaml:"defaults,omitempty"`  // 文章字段默认值

	path string
	file *Config // 配置文件里的原始值，Save 只写回这一份，避免把环境变量、参数持久化
}

// Defaults 文章字段默认值：命令行参数和 frontmatter 都没写时使用
type Defaults struct {
	Author      string `yaml:"author,omitempty" json:"author"`
	OpenComment *bool  `yaml:"open_comment,omitempty" json:"open_comment"`
	FansComment *bool  `yaml:"fans_comment,omitempty" json:"fans_comment"`
}

// apply 把默认值填进 m 里没写的字段
func (d Defaults) apply(m *Meta) {
	if m.Author == "" {
		m.Author = d.Author
	}
	if m.OpenComment == nil {
		m.OpenComment = d.OpenComment
	}
	if m.FansComment == nil {
		m.FansComment = d.FansComment
	}
}

// configKeys wxctl config set 支持的键
var configKeys = []string{"author", "open_comment", "fans_comment", "skin"}

// setKey 修改一个配置项，value 为空表示清除
func (c *Config) setKey(key, value string) error {
	parseBool := func() (*bool, error) {
		if value == "" {
			return nil, nil
		}
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, usageError(fmt.Sprintf("%s must be true or false, got %q", key, value))
		}
		return &b, nil
	}
	var err error
	switch key {
	case "author":
		if n := utf8.RuneCountInString(value); n > maxAuthor {
			return usageError(fmt.Sprintf("author 不超过 %d 个字，现在 %d 个", maxAuthor, n))
		}
		c.Defaults.Author = value
	case "open_comment":
		c.Defaults.OpenComment, err = parseBool()
	case "fans_comment":
		c.Defaults.FansComment, err = parseBool()
	case "skin":
		c.Skin = value
	default:
		return usageError(fmt.Sprintf("unknown key %q, available: %s", key, strings.Join(configKeys, ", ")))
	}
	return err
}

// Flags 全局参数，空字符串表示未指定
type Flags struct {
	Config, Mode, URL, Token string
}

// DefaultConfigPath 本地模式默认 ~/.config/wx-bot/config.yaml
func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if home, herr := os.UserHomeDir(); herr == nil {
		// macOS 的 UserConfigDir 是 ~/Library/Application Support，统一用 ~/.config
		dir, err = filepath.Join(home, ".config"), nil
	}
	if err != nil {
		return "wx-bot.yaml"
	}
	return filepath.Join(dir, "wx-bot", "config.yaml")
}

// LoadConfig 读取配置并叠加环境变量和命令行参数。配置文件不存在不算错误
func LoadConfig(f Flags) (*Config, error) {
	path := first(f.Config, os.Getenv("WXBOT_CONFIG"), DefaultConfigPath())
	c := &Config{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	raw := *c
	c.file = &raw

	c.Mode = first(f.Mode, os.Getenv("WXBOT_MODE"), c.Mode, ModeLocal)
	if c.Mode != ModeLocal && c.Mode != ModeServer {
		return nil, fmt.Errorf("mode must be %s or %s, got %q", ModeLocal, ModeServer, c.Mode)
	}
	defURL := defaultLocalURL
	if c.Mode == ModeServer {
		defURL = defaultServerURL
	}
	c.URL = strings.TrimRight(first(f.URL, os.Getenv("WXBOT_URL"), c.URL, defURL), "/")
	c.Token = first(f.Token, os.Getenv("WXBOT_TOKEN"), c.Token)
	if c.Mode == ModeServer && c.Token == "" {
		// 服务器上可以直接复用 wx-bot 服务自己的 token
		c.Token = os.Getenv("GZH_TOKEN")
	}
	c.Skin = first(os.Getenv("WXBOT_SKIN"), c.Skin)
	if c.CacheDir == "" {
		c.CacheDir = filepath.Join(filepath.Dir(path), "cache")
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// Save 修改配置文件里的值并写回，权限 0600（里面有 token）
func (c *Config) Save(update func(f *Config)) error {
	update(c.file)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c.file)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// CheckPerm 配置文件对其他用户可读时给出提示
func (c *Config) CheckPerm() string {
	st, err := os.Stat(c.path)
	if err != nil || st.Mode().Perm()&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("warning: %s is readable by other users, run: chmod 600 %s", c.path, c.path)
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return strings.Repeat("*", len(t))
	}
	return t[:4] + strings.Repeat("*", len(t)-8) + t[len(t)-4:]
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
