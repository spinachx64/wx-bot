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
	"wx-bot/internal/wechat"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	appID, secret, token := os.Getenv("GZH_APPID"), os.Getenv("GZH_SECRET"), os.Getenv("GZH_TOKEN")
	if appID == "" || secret == "" || token == "" {
		log.Error("GZH_APPID, GZH_SECRET and GZH_TOKEN must be set")
		os.Exit(1)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// SKIN_DIR 可选：目录下的 *.json 作为额外皮肤，同名覆盖内置
	skins, err := render.LoadRegistry(os.Getenv("SKIN_DIR"))
	if err != nil {
		log.Error("load skins", "err", err)
		os.Exit(1)
	}

	wx := wechat.New(appID, secret)
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(wx, render.New(skins), token, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", addr)
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
