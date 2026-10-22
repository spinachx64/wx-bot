package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

const (
	appA = "wx0123456789abcdef"
	appB = "wxfedcba9876543210"
	sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, testKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestTenantAndTokenLifecycle(t *testing.T) {
	s, dir := openTest(t)
	ctx := context.Background()

	a, err := s.CreateTenant(ctx, "alpha", appA, "secretA")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Dir) != 32 || strings.Contains(a.Dir, appA) {
		t.Fatalf("dir should be random hex: %q", a.Dir)
	}
	if st, err := os.Stat(filepath.Join(dir, "uploads", a.Dir)); err != nil || !st.IsDir() {
		t.Fatalf("tenant dir not created: %v", err)
	}
	if _, err := s.CreateTenant(ctx, "alpha", appB, "x"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := s.CreateTenant(ctx, "Bad Name", appB, "x"); err == nil {
		t.Fatal("invalid name accepted")
	}
	b, _ := s.CreateTenant(ctx, "beta", appB, "secretB")

	plain, tok, err := s.CreateToken(ctx, a.ID, "laptop")
	if err != nil || !strings.HasPrefix(plain, "wxt_") || tok.Prefix != plain[:12] {
		t.Fatalf("create token: %v %q %+v", err, plain, tok)
	}
	got, err := s.Authenticate(ctx, plain)
	if err != nil || got.ID != a.ID || got.Secret != "secretA" {
		t.Fatalf("authenticate: %v %+v", err, got)
	}
	if _, err := s.Authenticate(ctx, "wxt_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad token: %v", err)
	}

	// 别的租户不能吊销 a 的 token
	if err := s.DeleteToken(ctx, b.ID, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant delete: %v", err)
	}

	// 停用后 token 失效
	yes := true
	if _, err := s.UpdateTenant(ctx, a.ID, nil, &yes); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, plain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled tenant should fail auth: %v", err)
	}

	// 删除租户级联删除 token 和目录
	if err := s.DeleteTenant(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if toks, _ := s.ListTokens(ctx, a.ID); len(toks) != 0 {
		t.Fatalf("tokens not cascaded: %v", toks)
	}
	if _, err := os.Stat(filepath.Join(dir, "uploads", a.Dir)); !os.IsNotExist(err) {
		t.Fatalf("tenant dir not removed: %v", err)
	}
}

func TestSecretEncryptedAndKeyBound(t *testing.T) {
	s, dir := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateTenant(ctx, "alpha", appA, "super-secret-value"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, _ := os.ReadFile(filepath.Join(dir, "wx-bot.db"))
	wal, _ := os.ReadFile(filepath.Join(dir, "wx-bot.db-wal"))
	if strings.Contains(string(db)+string(wal), "super-secret-value") {
		t.Fatal("secret stored in plaintext")
	}

	other := []byte("ffffffffffffffffffffffffffffffff")
	s2, err := Open(dir, other)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.GetTenant(ctx, "alpha"); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("wrong key should fail to decrypt: %v", err)
	}
}

func TestImportToken(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	a, _ := s.CreateTenant(ctx, "default", appA, "x")
	for i := 0; i < 2; i++ { // 重复导入不报错
		if err := s.ImportToken(ctx, a.ID, "legacy-token-123", "GZH_TOKEN"); err != nil {
			t.Fatal(err)
		}
	}
	if toks, _ := s.ListTokens(ctx, a.ID); len(toks) != 1 {
		t.Fatalf("want 1 token, got %d", len(toks))
	}
	if got, err := s.Authenticate(ctx, "legacy-token-123"); err != nil || got.ID != a.ID {
		t.Fatalf("legacy token: %v", err)
	}
}

func TestUploadIsolationAndCleanup(t *testing.T) {
	s, dir := openTest(t)
	ctx := context.Background()
	a, _ := s.CreateTenant(ctx, "alpha", appA, "x")
	b, _ := s.CreateTenant(ctx, "beta", appB, "x")

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.Local)
	s.now = func() time.Time { return now.AddDate(0, -2, 0) } // 8 月
	if _, err := s.SaveUpload(ctx, a, KindImage, sha1, ".png", []byte("old"), "", []byte(`{"url":"u-old"}`)); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.AddDate(0, -1, 0) } // 9 月
	if _, err := s.SaveUpload(ctx, b, KindImage, sha1, ".png", []byte("keep"), "", []byte(`{"url":"u-b"}`)); err != nil {
		t.Fatal(err)
	}

	// 同一 hash 在另一个租户下不可见
	if _, err := s.FindUpload(ctx, b.ID, KindMaterial, sha1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kind isolation: %v", err)
	}
	ua, _ := s.FindUpload(ctx, a.ID, KindImage, sha1)
	ub, _ := s.FindUpload(ctx, b.ID, KindImage, sha1)
	if string(ua.Result) == string(ub.Result) || !strings.HasPrefix(ua.Path, a.Dir+"/") || !strings.HasPrefix(ub.Path, b.Dir+"/") {
		t.Fatalf("tenant isolation broken: %+v %+v", ua, ub)
	}

	// 用户文件名不进入路径
	if _, err := s.SaveUpload(ctx, a, KindImage, "../../etc/passwd", ".png", nil, "", nil); err == nil {
		t.Fatal("bad sha accepted")
	}
	if _, err := s.SaveUpload(ctx, a, KindImage, sha1, "/../x", nil, "", nil); err == nil {
		t.Fatal("bad ext accepted")
	}

	// 10 月清理：保留 9、10 月，删掉 8 月
	s.now = func() time.Time { return now }
	if got := RetentionCutoff(now); got != "2026-09" {
		t.Fatalf("cutoff = %s", got)
	}
	n, err := s.Cleanup(ctx)
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	if _, err := s.FindUpload(ctx, a.ID, KindImage, sha1); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired record kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "uploads", a.Dir, "2026-08")); !os.IsNotExist(err) {
		t.Fatal("expired month dir kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "uploads", ub.Path)); err != nil {
		t.Fatalf("current file removed: %v", err)
	}
	// 1 月跨年
	if got := RetentionCutoff(time.Date(2027, 1, 5, 0, 0, 0, 0, time.Local)); got != "2026-12" {
		t.Fatalf("cross-year cutoff = %s", got)
	}
}
