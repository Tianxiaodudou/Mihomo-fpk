package main

import (
	"io/fs"
	"strings"
	"testing"
)

// 回归测试：网关以「无尾斜杠」的 /app/<appname> 作为 iframe 地址时，
// 相对资源引用必须被改写为带前缀的绝对路径，否则浏览器会去请求
// /app/assets/... 导致 404、整页黑屏。
func TestRewriteAssetBase(t *testing.T) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := rewriteAssetBase(string(b), "/app/MihomoProxy")
	if strings.Contains(html, `"./assets/`) {
		t.Fatalf("仍存在相对资源引用: %s", html)
	}
	if !strings.Contains(html, `"/app/MihomoProxy/assets/`) {
		t.Fatalf("未生成带前缀的绝对资源路径: %s", html)
	}
	// 资源确实存在于嵌入文件系统
	for _, line := range strings.Split(html, "\n") {
		i := strings.Index(line, "/app/MihomoProxy/assets/")
		if i < 0 {
			continue
		}
		name := strings.Trim(strings.Fields(line[strings.Index(line, "/app/MihomoProxy/assets/"):])[0], `"'>\r`)
		name = strings.TrimPrefix(name, "/app/MihomoProxy/")
		if _, err := sub.Open(name); err != nil {
			t.Errorf("资源在包内不存在: %s (%v)", name, err)
		}
	}
	// 前缀为空（本地根路径访问）时保持原样
	if got := rewriteAssetBase(string(b), ""); got != string(b) {
		t.Error("空前缀时不应改写")
	}
}
