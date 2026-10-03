package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:static
var staticFS embed.FS

// StaticHandler 提供嵌入的 Vue 单页应用。
// StaticHandler 提供嵌入的 Vue 单页应用。
// prefix 为统一网关挂载前缀（如 /app/MihomoProxy）。桌面/网关 iframe 的地址是
// 「无尾斜杠」的 /app/MihomoProxy，此时页面里的相对资源 "./assets/x.js" 会被
// 浏览器解析成 /app/assets/x.js（404），前端脚本加载失败 → 整页黑屏。
// 因此这里在服务 index.html 时把相对资源引用改写成「前缀 + 绝对路径」。
func StaticHandler(prefix string) (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "" {
			serveIndex(w, sub, prefix)
			return
		}
		clean := strings.TrimPrefix(p, "/")
		if f, err := sub.Open(clean); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA 回退：未知路径交给前端路由
		serveIndex(w, sub, prefix)
	}), nil
}

// rewriteAssetBase 把 index.html 中的相对资源引用改写为带网关前缀的绝对路径。
// 前缀为空（本地直接访问根路径）时保持原样。
func rewriteAssetBase(html, prefix string) string {
	p := strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	if p == "" || p == "/" {
		return html
	}
	r := strings.NewReplacer(
		`url('./`, `url('`+p+`/`,
		`url(./`, `url(`+p+`/`,
		`"./`, `"`+p+`/`,
		`'./`, `'`+p+`/`,
	)
	return r.Replace(html)
}

func serveIndex(w http.ResponseWriter, sub fs.FS, prefix string) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "前端资源缺失", http.StatusInternalServerError)
		return
	}
	html := rewriteAssetBase(string(b), prefix)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(html))
}
