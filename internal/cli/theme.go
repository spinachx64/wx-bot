package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxThemeCSS 与服务端 render.MaxCustomCSS 一致
const maxThemeCSS = 256 << 10

// isThemeFile 主题值以 .css 结尾时视为本地 CSS 文件，否则是服务端主题名
func isThemeFile(v string) bool { return strings.HasSuffix(strings.ToLower(v), ".css") }

// themePath 相对路径以 base 为基准；~/ 展开为家目录
func themePath(v, base string) string {
	if rest, ok := strings.CutPrefix(v, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	if filepath.IsAbs(v) || base == "" {
		return v
	}
	return filepath.Join(base, v)
}

// readThemeCSS 读取本地主题文件，检查大小
func readThemeCSS(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("theme file: %w", err)
	}
	if st.Size() > maxThemeCSS {
		return "", fmt.Errorf("theme file %s is %d bytes, limit %d", path, st.Size(), maxThemeCSS)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("theme file: %w", err)
	}
	return string(data), nil
}

// resolveTheme 按优先级 参数 > frontmatter > 配置 取主题。
// 结果是主题名或本地 CSS 内容二选一；CSS 每次实时读取，随请求上传，服务端渲染完即丢弃。
// 相对路径：参数以当前目录为准，frontmatter 以文章所在目录为准，配置里存的是绝对路径
func (e *env) resolveTheme(flag string, a *Article) (name, css string, err error) {
	v, base := flag, ""
	switch {
	case flag != "":
	case a.Meta.Theme != "":
		v, base = a.Meta.Theme, a.Dir
	default:
		v = e.cfg.Theme
	}
	if !isThemeFile(v) {
		return v, "", nil
	}
	css, err = readThemeCSS(themePath(v, base))
	return "", css, err
}
