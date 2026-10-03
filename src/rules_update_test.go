package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountRuleLines(t *testing.T) {
	if n := countRuleLines([]byte("payload:\n- a\n- b\n")); n != 2 {
		t.Fatalf("countRuleLines = %d, want 2", n)
	}
	if n := countRuleLines(nil); n != 0 {
		t.Fatalf("空内容应为 0，得到 %d", n)
	}
}

// 未手动更新时：内置快照 = 规则版本，且各规则集条数来自 embed。
func TestRulesInfoBuiltin(t *testing.T) {
	info := RulesInfo(t.TempDir())
	if info["origin"] != "builtin" {
		t.Fatalf("origin = %v, want builtin", info["origin"])
	}
	files, ok := info["files"].(map[string]int)
	if !ok {
		t.Fatalf("files 类型错误: %T", info["files"])
	}
	for _, rp := range ruleProviderDefs {
		if files[rp.Name] < 10 {
			t.Fatalf("规则集 %s 条数异常: %d", rp.Name, files[rp.Name])
		}
	}
	if info["total"].(int) <= 0 {
		t.Fatal("total 应为正数")
	}
}

// 手动更新过的规则必须保留：重建配置时不得被内置快照覆盖。
func TestEnsureRuleFilesKeepsOnlineRules(t *testing.T) {
	varDir := t.TempDir()
	if _, err := EnsureRuleFiles(varDir); err != nil {
		t.Fatal(err)
	}
	dir := rulesDir(varDir)
	marker := []byte("payload:\n- +.upstream-updated.example.com\n")
	if err := os.WriteFile(filepath.Join(dir, "gfw.yaml"), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesMetaFile(dir), []byte(`{"origin":"online","updated_at":"2026-10-03T00:00:00Z","files":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRuleFiles(varDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "gfw.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "upstream-updated.example.com") {
		t.Fatal("在线更新的规则被内置快照覆盖了")
	}
	ver, origin := RulesVersion(varDir)
	if origin != "online" || ver != "2026-10-03" {
		t.Fatalf("RulesVersion = (%q,%q), want (2026-10-03,online)", ver, origin)
	}
	// meta 失效（如文件缺失）时必须回落到内置
	if err := os.Remove(filepath.Join(dir, "private.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, origin := RulesVersion(varDir); origin != "builtin" {
		t.Fatalf("规则文件缺失时应回落内置，得到 %q", origin)
	}
}

func TestRulesMirrorsNonEmpty(t *testing.T) {
	if len(rulesMirrors) < 2 {
		t.Fatal("至少要有主源 + 备用镜像")
	}
	for _, m := range rulesMirrors {
		if !strings.HasPrefix(m, "https://") || !strings.Contains(m, "%s") {
			t.Fatalf("镜像模板不合法: %s", m)
		}
	}
}
