package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// saveMediaID 只修改 frontmatter，保留正文、BOM、换行格式和文件权限。
func (a *Article) saveMediaID(id string) error {
	return a.saveDraftMeta(id, false)
}

func (a *Article) saveDraftMeta(id string, saveType bool) error {
	path, err := filepath.EvalSymlinks(a.Path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, a.source) {
		return errors.New("source file changed during upload; refusing to overwrite it")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	bom := []byte(nil)
	data := current
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		bom, data = data[:3], data[3:]
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	var out []byte
	if match := frontmatterRe.FindSubmatchIndex(data); match != nil {
		var doc yaml.Node
		if err := yaml.Unmarshal(data[match[2]:match[3]], &doc); err != nil {
			return err
		}
		if len(doc.Content) == 0 {
			doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		}
		mapping := doc.Content[0]
		if mapping.Kind != yaml.MappingNode {
			return errors.New("frontmatter must be a mapping")
		}
		found := false
		for i := 0; i < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == "media_id" {
				value := mapping.Content[i+1]
				value.Kind, value.Tag, value.Value, value.Style = yaml.ScalarNode, "!!str", id, 0
				value.Content = nil
				found = true
			}
		}
		if !found {
			mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "media_id"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id})
		}
		if saveType {
			found := false
			for i := 0; i < len(mapping.Content); i += 2 {
				if mapping.Content[i].Value == "article_type" {
					value := mapping.Content[i+1]
					value.Kind, value.Tag, value.Value, value.Style = yaml.ScalarNode, "!!str", a.Meta.articleType(), 0
					value.Content = nil
					found = true
					break
				}
			}
			if !found {
				mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "article_type"}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: a.Meta.articleType()})
			}
		}
		var front bytes.Buffer
		enc := yaml.NewEncoder(&front)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		text := strings.ReplaceAll(front.String(), "\n", newline)
		out = append(out, data[:match[2]]...)
		out = append(out, strings.TrimSuffix(text, newline)...)
		out = append(out, data[match[3]:]...)
	} else {
		value, err := yaml.Marshal(id)
		if err != nil {
			return err
		}
		out = []byte("---" + newline + "media_id: " + strings.TrimSpace(string(value)) + newline + "---" + newline)
		if saveType {
			out = []byte("---" + newline + "media_id: " + strings.TrimSpace(string(value)) + newline + "article_type: " + a.Meta.articleType() + newline + "---" + newline)
		}
		out = append(out, data...)
	}
	out = append(append([]byte(nil), bom...), out...)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wxctl-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.Write(out)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// 上传期间的修改已在上面检查；写入临时文件后再次检查，避免覆盖编辑器刚保存的内容。
	latest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(latest, current) {
		return errors.New("source file changed before writeback; refusing to overwrite it")
	}
	return os.Rename(tmp.Name(), path)
}
