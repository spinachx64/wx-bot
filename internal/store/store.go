// Package store 用 SQLite 保存租户（公众号）、访问 token 和上传记录，
// 并管理每个租户独立的图片目录。
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
	ErrInvalid  = errors.New("invalid input")
)

const schema = `
CREATE TABLE IF NOT EXISTS tenants (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE,
	appid      TEXT    NOT NULL UNIQUE,
	secret_enc BLOB    NOT NULL,
	dir        TEXT    NOT NULL UNIQUE,
	disabled   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
	id           INTEGER PRIMARY KEY,
	tenant_id    INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
	hash         TEXT    NOT NULL UNIQUE,
	prefix       TEXT    NOT NULL,
	note         TEXT    NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS uploads (
	id         INTEGER PRIMARY KEY,
	tenant_id  INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
	kind       TEXT    NOT NULL,
	sha256     TEXT    NOT NULL,
	month      TEXT    NOT NULL,
	path       TEXT    NOT NULL,
	size       INTEGER NOT NULL,
	media_id   TEXT    NOT NULL DEFAULT '',
	result     TEXT    NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (tenant_id, kind, sha256)
);
CREATE INDEX IF NOT EXISTS uploads_month ON uploads(month);
`

type Store struct {
	db        *sql.DB
	aead      cipher.AEAD
	uploadDir string
	now       func() time.Time
}

// Open 打开（必要时创建）dataDir 下的 wx-bot.db 和 uploads 目录。
// masterKey 是 32 字节密钥，用来加密公众号 secret
func Open(dataDir string, masterKey []byte) (*Store, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	uploadDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "wx-bot.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者，一个连接最简单，也避免 pragma 只作用在部分连接上
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db, aead: aead, uploadDir: uploadDir, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// ParseMasterKey 接受 64 位 hex 或 base64 编码的 32 字节密钥
func ParseMasterKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if b, err := hex.DecodeString(v); err == nil && len(b) == 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("master key must be 32 bytes, hex (64 chars) or base64 encoded")
}

// secret 加密时以 appid 作为附加数据，密文挪到别的租户上无法解密
func (s *Store) seal(appID, secret string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(secret), []byte("wx-bot/secret/"+appID)), nil
}

func (s *Store) open(appID string, enc []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(enc) < n {
		return "", errors.New("corrupt secret")
	}
	plain, err := s.aead.Open(nil, enc[:n], enc[n:], []byte("wx-bot/secret/"+appID))
	if err != nil {
		return "", errors.New("decrypt secret failed: wrong master key?")
	}
	return string(plain), nil
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
