package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tenant 一个公众号。Secret 只在服务内部使用，不输出到接口
type Tenant struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	AppID     string    `json:"appid"`
	Dir       string    `json:"-"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	Secret    string    `json:"-"`
}

// Token 访问 token 的元数据，明文只在创建时返回一次
type Token struct {
	ID         int64     `json:"id"`
	TenantID   int64     `json:"tenant_id"`
	Prefix     string    `json:"prefix"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
}

var (
	nameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	appIDRe = regexp.MustCompile(`^wx[0-9a-f]{16}$`)
)

func ValidName(n string) bool  { return nameRe.MatchString(n) }
func ValidAppID(a string) bool { return appIDRe.MatchString(a) }

// CreateTenant 新增租户并生成 128 位随机目录名
func (s *Store) CreateTenant(ctx context.Context, name, appID, secret string) (*Tenant, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: name %q, use 1-32 chars of a-z 0-9 _ -", ErrInvalid, name)
	}
	if !ValidAppID(appID) {
		return nil, fmt.Errorf("%w: appid %q", ErrInvalid, appID)
	}
	if secret == "" {
		return nil, fmt.Errorf("%w: secret is required", ErrInvalid)
	}
	enc, err := s.seal(appID, secret)
	if err != nil {
		return nil, err
	}
	dir, err := randHex(16)
	if err != nil {
		return nil, err
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tenants (name, appid, secret_enc, dir, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, appID, enc, dir, now.Unix())
	if isUnique(err) {
		return nil, fmt.Errorf("%w: tenant name or appid", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(s.uploadDir, dir), 0o700); err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Tenant{ID: id, Name: name, AppID: appID, Dir: dir, CreatedAt: time.Unix(now.Unix(), 0), Secret: secret}, nil
}

const tenantCols = `id, name, appid, secret_enc, dir, disabled, created_at`

func (s *Store) scanTenant(row interface{ Scan(...any) error }) (*Tenant, error) {
	var t Tenant
	var enc []byte
	var created int64
	if err := row.Scan(&t.ID, &t.Name, &t.AppID, &enc, &t.Dir, &t.Disabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.CreatedAt = time.Unix(created, 0)
	secret, err := s.open(t.AppID, enc)
	if err != nil {
		return nil, fmt.Errorf("tenant %s: %w", t.Name, err)
	}
	t.Secret = secret
	return &t, nil
}

// GetTenant 按 id 或 name 查找
func (s *Store) GetTenant(ctx context.Context, ref string) (*Tenant, error) {
	return s.scanTenant(s.db.QueryRowContext(ctx,
		`SELECT `+tenantCols+` FROM tenants WHERE name = ? OR CAST(id AS TEXT) = ?`, ref, ref))
}

func (s *Store) ListTenants(ctx context.Context) ([]*Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tenantCols+` FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t, err := s.scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTenant 修改 secret 和/或停用状态；nil 表示不改
func (s *Store) UpdateTenant(ctx context.Context, id int64, secret *string, disabled *bool) (*Tenant, error) {
	t, err := s.GetTenant(ctx, fmt.Sprint(id))
	if err != nil {
		return nil, err
	}
	if secret != nil {
		if *secret == "" {
			return nil, fmt.Errorf("%w: secret must not be empty", ErrInvalid)
		}
		enc, err := s.seal(t.AppID, *secret)
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE tenants SET secret_enc = ? WHERE id = ?`, enc, id); err != nil {
			return nil, err
		}
	}
	if disabled != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE tenants SET disabled = ? WHERE id = ?`, *disabled, id); err != nil {
			return nil, err
		}
	}
	return s.GetTenant(ctx, fmt.Sprint(id))
}

// DeleteTenant 删除租户、它的 token、上传记录和图片目录
func (s *Store) DeleteTenant(ctx context.Context, id int64) error {
	t, err := s.GetTenant(ctx, fmt.Sprint(id))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.uploadDir, t.Dir))
}

// CreateToken 为租户生成 token，返回明文（只此一次）
func (s *Store) CreateToken(ctx context.Context, tenantID int64, note string) (string, *Token, error) {
	raw, err := randHex(24)
	if err != nil {
		return "", nil, err
	}
	plain := "wxt_" + raw
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (tenant_id, hash, prefix, note, created_at) VALUES (?, ?, ?, ?, ?)`,
		tenantID, hashToken(plain), plain[:12], note, now.Unix())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isFK(err) {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	return plain, &Token{ID: id, TenantID: tenantID, Prefix: plain[:12], Note: note, CreatedAt: time.Unix(now.Unix(), 0)}, nil
}

// ImportToken 把已有的明文 token 登记到租户（迁移旧的 GZH_TOKEN 用），已存在则忽略
func (s *Store) ImportToken(ctx context.Context, tenantID int64, plain, note string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO tokens (tenant_id, hash, prefix, note, created_at) VALUES (?, ?, ?, ?, ?)`,
		tenantID, hashToken(plain), prefix(plain), note, s.now().Unix())
	return err
}

func prefix(t string) string {
	if len(t) > 6 {
		return t[:6]
	}
	return ""
}

func (s *Store) ListTokens(ctx context.Context, tenantID int64) ([]*Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tenant_id, prefix, note, created_at, last_used_at FROM tokens WHERE tenant_id = ? ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		var t Token
		var created, used int64
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Prefix, &t.Note, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(created, 0)
		if used > 0 {
			t.LastUsedAt = time.Unix(used, 0)
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// DeleteToken 吊销 token；必须属于该租户
func (s *Store) DeleteToken(ctx context.Context, tenantID, tokenID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ? AND tenant_id = ?`, tokenID, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Authenticate 用明文 token 找到所属租户；停用的租户视为无效。
// last_used_at 最多每分钟写一次，避免每个请求都写库
func (s *Store) Authenticate(ctx context.Context, plain string) (*Tenant, error) {
	if plain == "" {
		return nil, ErrNotFound
	}
	h := hashToken(plain)
	var tokenID, used int64
	t, err := s.scanTenantWith(ctx, h, &tokenID, &used)
	if err != nil {
		return nil, err
	}
	if t.Disabled {
		return nil, ErrNotFound
	}
	if now := s.now().Unix(); now-used >= 60 {
		s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, now, tokenID)
	}
	return t, nil
}

func (s *Store) scanTenantWith(ctx context.Context, hash string, tokenID, used *int64) (*Tenant, error) {
	var t Tenant
	var enc []byte
	var created int64
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.appid, t.secret_enc, t.dir, t.disabled, t.created_at, k.id, k.last_used_at
		FROM tokens k JOIN tenants t ON t.id = k.tenant_id WHERE k.hash = ?`, hash).
		Scan(&t.ID, &t.Name, &t.AppID, &enc, &t.Dir, &t.Disabled, &created, tokenID, used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(created, 0)
	if t.Secret, err = s.open(t.AppID, enc); err != nil {
		return nil, err
	}
	return &t, nil
}

func isFK(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
