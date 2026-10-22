// wxctl 是 wx-bot 的命令行客户端，本地模式通过 HTTPS 调线上服务，
// 服务器模式供同机的其他程序调用（默认 JSON 输出）。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"wx-bot/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
