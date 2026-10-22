package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wx-bot/internal/render"
	"wx-bot/internal/server"
	"wx-bot/internal/store"
)

// 环境变量：
//
//	WXBOT_MASTER_KEY   32 字节密钥（hex 或 base64），加密公众号 secret，必填
//	WXBOT_ADMIN_TOKEN  管理员 token，用于 /api/v1/admin/；不设则管理接口全部拒绝
//	DATA_DIR           数据目录（wx-bot.db 和 uploads/），默认 ./data
//	LISTEN_ADDR        监听地址，默认 :8080
//	SKIN_DIR           额外皮肤目录，可选
//	GZH_APPID / GZH_SECRET / GZH_TOKEN  旧的单租户配置，启动时迁移为 default 租户
func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	key, err := store.ParseMasterKey(os.Getenv("WXBOT_MASTER_KEY"))
	if err != nil {
		log.Error("WXBOT_MASTER_KEY", "err", err)
		os.Exit(1)
	}
	adminToken := os.Getenv("WXBOT_ADMIN_TOKEN")
	if adminToken == "" {
		log.Warn("WXBOT_ADMIN_TOKEN not set, admin API disabled")
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	st, err := store.Open(dataDir, key)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := migrateLegacy(context.Background(), st, log); err != nil {
		log.Error("migrate legacy config", "err", err)
		os.Exit(1)
	}

	skins, err := render.LoadRegistry(os.Getenv("SKIN_DIR"))
	if err != nil {
		log.Error("load skins", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(st, render.New(skins), adminToken, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 图片保留当月和上个月，每天清理一次
	go st.RunCleanup(ctx, func(n int64, err error) {
		if err != nil {
			log.Warn("cleanup uploads", "removed", n, "err", err)
		} else if n > 0 {
			log.Info("cleanup uploads", "removed", n)
		}
	})

	go func() {
		log.Info("listening", "addr", addr, "data", dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

// migrateLegacy 把 GZH_APPID/GZH_SECRET/GZH_TOKEN 登记为 default 租户。
// 可重复执行：租户已存在时只同步 secret，token 已存在时忽略
func migrateLegacy(ctx context.Context, st *store.Store, log *slog.Logger) error {
	appID, secret, token := os.Getenv("GZH_APPID"), os.Getenv("GZH_SECRET"), os.Getenv("GZH_TOKEN")
	if appID == "" || secret == "" {
		return nil
	}
	t, err := st.GetTenant(ctx, "default")
	switch {
	case errors.Is(err, store.ErrNotFound):
		if t, err = st.CreateTenant(ctx, "default", appID, secret); err != nil {
			return err
		}
		log.Info("migrated legacy config to tenant default", "appid", appID)
	case err != nil:
		return err
	case t.AppID != appID:
		log.Warn("tenant default has a different appid, GZH_APPID ignored", "db", t.AppID, "env", appID)
		return nil
	case t.Secret != secret:
		if t, err = st.UpdateTenant(ctx, t.ID, &secret, nil); err != nil {
			return err
		}
	}
	if token != "" {
		return st.ImportToken(ctx, t.ID, token, "GZH_TOKEN")
	}
	return nil
}
