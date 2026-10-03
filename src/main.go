package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// P 为全局路径集合（由环境变量决定，不做任何硬编码）。
var P *Paths

func main() {
	socketFlag := flag.String("socket", "", "统一网关 Unix Socket 路径")
	flag.Parse()

	P = NewPaths()
	if err := P.EnsureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "创建运行目录失败:", err)
		os.Exit(1)
	}

	// 日志：标准输出 + var/web.log（便于应用中心查看）
	logPath := filepath.Join(P.Var, "web.log")
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stdout, lf))
		defer lf.Close()
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[MihomoProxy] ")

	socket := *socketFlag
	if socket == "" {
		socket = P.GatewaySocket
	}
	if socket == "" {
		socket = filepath.Join(P.AppDest, "app.sock")
	}

	app := NewApp(P)
	app.OnStartup()

	stopCh := make(chan struct{})
	go app.AutoLoop(stopCh)

	static, err := StaticHandler(P.GatewayPrefix)
	if err != nil {
		log.Fatalf("前端资源初始化失败: %v", err)
	}
	mux := http.NewServeMux()
	app.Register(mux)
	mux.Handle("/", static)

	handler := recoverMW(prefixMW(P.GatewayPrefix, mux))

	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		log.Fatalf("创建 Socket 目录失败: %v", err)
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatalf("监听统一网关 Socket 失败(%s): %v", socket, err)
	}
	_ = os.Chmod(socket, 0o660)
	log.Printf("后端已启动 socket=%s arch=%s prefix=%q etc=%s var=%s", socket, P.Arch, P.GatewayPrefix, P.Etc, P.Var)

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 60 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("服务退出: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("收到退出信号，正在停止…")
	close(stopCh)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	app.Mihomo.Stop()
	_ = os.Remove(socket)
	log.Printf("已退出")
}

// prefixMW 剥离统一网关前缀（若平台未剥离）。
func prefixMW(prefix string, next http.Handler) http.Handler {
	if prefix == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix {
			r.URL.Path = "/"
		} else if strings.HasPrefix(r.URL.Path, prefix+"/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
		}
		next.ServeHTTP(w, r)
	})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v (%s %s)", rec, r.Method, r.URL.Path)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"error":"内部错误"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func init() {
	_ = runtime.GOOS
}
