package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// 上传类型：正文图片（uploadimg 返回 url）和永久图片素材（返回 media_id）
const (
	KindImage    = "image"
	KindMaterial = "material"
)

// Upload 一次上传的记录。Result 是微信返回的原始 JSON，命中缓存时原样返回
type Upload struct {
	ID        int64
	TenantID  int64
	Kind      string
	SHA256    string
	Month     string
	Path      string
	Size      int64
	MediaID   string
	Result    []byte
	CreatedAt time.Time
}

var (
	shaRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	extRe   = regexp.MustCompile(`^\.[a-z0-9]{1,5}$`)
	monthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

func monthOf(t time.Time) string { return t.Format("2006-01") }

// FindUpload 按内容 hash 查找本租户的上传记录，没有返回 ErrNotFound
func (s *Store) FindUpload(ctx context.Context, tenantID int64, kind, sha string) (*Upload, error) {
	var u Upload
	var created int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, kind, sha256, month, path, size, media_id, result, created_at
		FROM uploads WHERE tenant_id = ? AND kind = ? AND sha256 = ?`, tenantID, kind, sha).
		Scan(&u.ID, &u.TenantID, &u.Kind, &u.SHA256, &u.Month, &u.Path, &u.Size, &u.MediaID, &u.Result, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

// SaveUpload 把文件写到 uploads/<租户随机目录>/<YYYY-MM>/<sha256><ext> 并记录结果。
// 文件名完全由服务端生成，不使用用户提供的文件名
func (s *Store) SaveUpload(ctx context.Context, t *Tenant, kind, sha, ext string, data []byte, mediaID string, result []byte) (*Upload, error) {
	if !shaRe.MatchString(sha) || !extRe.MatchString(ext) {
		return nil, fmt.Errorf("invalid sha or ext")
	}
	now := s.now()
	month := monthOf(now)
	rel := filepath.Join(t.Dir, month, kind+"-"+sha+ext)
	abs := filepath.Join(s.uploadDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, err
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO uploads (tenant_id, kind, sha256, month, path, size, media_id, result, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (tenant_id, kind, sha256) DO UPDATE SET
			month = excluded.month, path = excluded.path, size = excluded.size,
			media_id = excluded.media_id, result = excluded.result, created_at = excluded.created_at`,
		t.ID, kind, sha, month, rel, len(data), mediaID, result, now.Unix())
	if err != nil {
		return nil, err
	}
	return &Upload{TenantID: t.ID, Kind: kind, SHA256: sha, Month: month, Path: rel, Size: int64(len(data)),
		MediaID: mediaID, Result: result, CreatedAt: time.Unix(now.Unix(), 0)}, nil
}

// RetentionCutoff 保留当月和上个月，返回最早保留的月份
func RetentionCutoff(now time.Time) string {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return monthOf(first.AddDate(0, -1, 0))
}

// Cleanup 删除早于保留期的上传记录和月份目录，返回删除的记录数
func (s *Store) Cleanup(ctx context.Context) (int64, error) {
	cutoff := RetentionCutoff(s.now())
	res, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE month < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()

	// 记录删除后再按目录清理，顺带清掉没有记录的残留文件
	tenants, err := os.ReadDir(s.uploadDir)
	if err != nil {
		return n, err
	}
	var errs []error
	for _, td := range tenants {
		if !td.IsDir() {
			continue
		}
		months, err := os.ReadDir(filepath.Join(s.uploadDir, td.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, md := range months {
			if md.IsDir() && monthRe.MatchString(md.Name()) && md.Name() < cutoff {
				if err := os.RemoveAll(filepath.Join(s.uploadDir, td.Name(), md.Name())); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return n, errors.Join(errs...)
}

// RunCleanup 启动时清理一次，之后每天一次，直到 ctx 结束
func (s *Store) RunCleanup(ctx context.Context, logf func(n int64, err error)) {
	for {
		n, err := s.Cleanup(ctx)
		logf(n, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
