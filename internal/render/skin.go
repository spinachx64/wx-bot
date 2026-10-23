package render

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/aymerick/douceur/css"
	"github.com/aymerick/douceur/parser"
)

//go:embed skins/*.css
var builtinSkins embed.FS

const DefaultSkin = "summer-breeze"

var ErrUnknownSkin = errors.New("unknown skin")

// Skin 一套排版皮肤，即一份完整的 CSS。
// 渲染时文章包在 <section id="nice"> 里，CSS 规则按优先级内联到每个元素上，
// ::before / ::after 展开成真实的 <span>。选择器建议以 #nice 开头。
type Skin struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Warnings []string `json:"warnings,omitempty"` // 被跳过的规则，方便调试皮肤
	rules    []rule
}

type rule struct {
	sel    cascadia.Sel
	pseudo string // ""、"before"、"after"
	order  int
	decls  []decl
}

var (
	labelRe = regexp.MustCompile(`^\s*/\*\s*([^\n*]+)`)
	propRe  = regexp.MustCompile(`^-?[a-z][a-z0-9-]*$`)
	// 连续分号（中间可夹空白）
	emptyDeclRe = regexp.MustCompile(`;(\s*;)+`)
)

func parseSkin(name string, data []byte) (*Skin, error) {
	s := &Skin{Name: name, Label: name}
	if m := labelRe.FindSubmatch(data); m != nil {
		// 首个注释的第一行作为显示名，「夏日海风 —— 说明」只取破折号前
		label, _, _ := strings.Cut(string(m[1]), "——")
		if label = strings.TrimSpace(label); label != "" {
			s.Label = label
		}
	}

	// mdnice 主题里常见「color: red;;」，douceur 遇到空声明会直接报错
	sheet, err := parser.Parse(emptyDeclRe.ReplaceAllString(string(data), ";"))
	if err != nil {
		return nil, fmt.Errorf("skin %s: %w", name, err)
	}
	for _, r := range sheet.Rules {
		if r.Kind != css.QualifiedRule {
			s.warn("skip at-rule %s", r.Name)
			continue
		}
		decls := s.parseDecls(r.Declarations)
		for _, text := range r.Selectors {
			sel, err := cascadia.ParseWithPseudoElement(text)
			if err != nil {
				s.warn("skip selector %q: %v", text, err)
				continue
			}
			pseudo := sel.PseudoElement()
			if pseudo != "" && pseudo != "before" && pseudo != "after" {
				s.warn("skip selector %q: unsupported pseudo-element", text)
				continue
			}
			s.rules = append(s.rules, rule{sel: sel, pseudo: pseudo, order: len(s.rules), decls: decls})
		}
	}
	if len(s.rules) == 0 {
		return nil, fmt.Errorf("skin %s: no usable rules", name)
	}
	return s, nil
}

func (s *Skin) parseDecls(in []*css.Declaration) []decl {
	out := make([]decl, 0, len(in))
	for _, d := range in {
		prop := strings.ToLower(strings.TrimSpace(d.Property))
		val := strings.TrimSpace(d.Value)
		lv := strings.ToLower(val)
		if !propRe.MatchString(prop) || strings.Contains(lv, "javascript:") || strings.Contains(lv, "expression(") {
			s.warn("skip declaration %s: %s", prop, val)
			continue
		}
		// 内联到 style="" 里，双引号统一换成单引号，避免被转义成 &#34;
		if prop != "content" {
			val = strings.ReplaceAll(val, `"`, "'")
		}
		out = append(out, decl{prop: prop, val: val, important: d.Important})
	}
	return out
}

func (s *Skin) warn(format string, args ...any) {
	s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...))
}

// Registry 可用皮肤集合
type Registry struct {
	skins map[string]*Skin
	names []string
}

// LoadRegistry 加载内置皮肤；dir 非空时再加载其中的 *.css，
// 同名文件覆盖内置皮肤。皮肤名取文件名。
func LoadRegistry(dir string) (*Registry, error) {
	files := map[string][]byte{}
	entries, err := builtinSkins.ReadDir("skins")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := builtinSkins.ReadFile("skins/" + e.Name())
		if err != nil {
			return nil, err
		}
		files[strings.TrimSuffix(e.Name(), ".css")] = data
	}
	if dir != "" {
		matches, err := filepath.Glob(filepath.Join(dir, "*.css"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				return nil, err
			}
			files[strings.TrimSuffix(filepath.Base(m), ".css")] = data
		}
	}
	if _, ok := files[DefaultSkin]; !ok {
		return nil, fmt.Errorf("default skin %s missing", DefaultSkin)
	}

	r := &Registry{skins: map[string]*Skin{}}
	for name, data := range files {
		s, err := parseSkin(name, data)
		if err != nil {
			return nil, err
		}
		r.skins[name] = s
		r.names = append(r.names, name)
	}
	sort.Strings(r.names)
	return r, nil
}

// Get 按名称取皮肤，空名称返回默认皮肤
func (r *Registry) Get(name string) (*Skin, error) {
	if name == "" {
		name = DefaultSkin
	}
	s, ok := r.skins[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSkin, name)
	}
	return s, nil
}

// List 按名称排序返回全部皮肤
func (r *Registry) List() []*Skin {
	out := make([]*Skin, len(r.names))
	for i, n := range r.names {
		out[i] = r.skins[n]
	}
	return out
}
