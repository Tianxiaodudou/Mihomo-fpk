package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKernelTagRE(t *testing.T) {
	valid := []string{"v1.19.33", "v1.19.32", "v1.0.0", "v1.19.33-alpha.1", "v0.0.0-local1729680000123456789", "v1.19.33-v2-go123"}
	for _, s := range valid {
		if !kernelTagRE.MatchString(s) {
			t.Fatalf("应接受版本号 %q", s)
		}
	}
	invalid := []string{"Prerelease-Alpha", "1.19.33", "v1.19", "v1.19.33/../x", "", "v1.19.33 "}
	for _, s := range invalid {
		if kernelTagRE.MatchString(s) {
			t.Fatalf("不应接受 %q", s)
		}
	}
}

func TestFilterKernelReleases(t *testing.T) {
	tag := "v1.19.33"
	generic := linuxAssetName(tag)
	variant := "mihomo-linux-" + runtime.GOARCH + "-v2-go123-" + tag + ".gz"
	if generic != "mihomo-linux-"+runtime.GOARCH+"-"+tag+".gz" {
		t.Fatalf("默认 asset 名不符合官方规则：%s", generic)
	}
	digest := "132f6f3edf9a65e62f04853aed3358472e0b2b7666daa8419edfa2ddf5bb6323"
	raw := []ghRelease{
		{
			TagName: tag, PublishedAt: "2026-10-21T16:56:13Z",
			Assets: []ghAsset{
				{Name: variant, Size: 111, Digest: "sha256:" + digest},
				{Name: generic, Size: 222},
				{Name: "mihomo-linux-" + runtime.GOARCH + "-" + tag + ".deb"},
			},
		},
		{
			TagName: "Prerelease-Alpha",
			Assets:  []ghAsset{{Name: "mihomo-linux-" + runtime.GOARCH + "-alpha-3f27ef3.gz"}},
		},
		{
			TagName: "v1.19.32",
			Assets:  []ghAsset{{Name: "mihomo-linux-" + runtime.GOARCH + "-compatible-v1.19.32.gz"}},
		},
	}
	got := filterKernelReleases(raw)
	if len(got) != 3 {
		t.Fatalf("应按编译变体展开为 3 条，实际 %d：%+v", len(got), got)
	}
	if got[0].Asset != generic || got[0].Label != tag {
		t.Fatalf("默认包应排在最前：%+v", got[0])
	}
	if got[1].Asset != variant || got[1].Label != "v2-go123-"+tag {
		t.Fatalf("变体展示格式不正确：%+v", got[1])
	}
	if got[1].AssetSHA256 != digest || got[0].AssetSHA256 != "" {
		t.Fatalf("digest 对应关系不正确：%+v %+v", got[0], got[1])
	}
	if got[2].Label != "compatible-v1.19.32" {
		t.Fatalf("compatible 变体展示不正确：%+v", got[2])
	}
	if key := variantKey(variant, tag); key != tag+"-v2-go123" {
		t.Fatalf("变体目录标识不正确：%s", key)
	}
	if key := variantKey(generic, tag); key != tag {
		t.Fatalf("默认包目录标识应为 tag：%s", key)
	}
	if normalizeSHA("sha256:xyz") != "" || normalizeSHA("") != "" {
		t.Fatalf("非法 digest 应被忽略")
	}
}

func TestReleaseFitsArch(t *testing.T) {
	good := KernelRelease{Tag: "v1.19.33", Asset: linuxAssetName("v1.19.33")}
	if !releaseFitsArch(good) {
		t.Fatalf("本机架构的包应通过：%s", good.Asset)
	}
	bad := KernelRelease{Tag: "v1.19.33", Asset: "mihomo-linux-otherarch-v1.19.33.gz"}
	if releaseFitsArch(bad) {
		t.Fatalf("其他架构的包不应通过：%s", bad.Asset)
	}
	if got := filterReleasesForArch([]KernelRelease{good, bad}); len(got) != 1 || got[0].Asset != good.Asset {
		t.Fatalf("架构过滤结果不正确：%+v", got)
	}
}

func TestKernelStateAndFallback(t *testing.T) {
	p := &Paths{AppDest: t.TempDir(), Var: t.TempDir(), Arch: "x86_64"}
	if err := saveKernelState(p, &KernelState{Version: "v1.19.33"}); err != nil {
		t.Fatal(err)
	}
	if st := loadKernelState(p); st == nil || st.Version != "v1.19.33" {
		t.Fatalf("选中状态读写失败：%+v", st)
	}

	// 文件不存在时应回落到包内自带内核
	if _, v := p.activeKernelBin(); v != "" {
		t.Fatalf("缺少内核文件时不应生效：%q", v)
	}
	if _, src := p.MihomoVersion(); src != "builtin" {
		t.Fatalf("缺少内核文件时应回落 builtin")
	}

	bin := p.KernelBin("v1.19.33")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeKernelMeta(p, &KernelMeta{Version: "v1.19.33", Asset: linuxAssetName("v1.19.33")}); err != nil {
		t.Fatal(err)
	}
	active, v := p.activeKernelBin()
	if v != "v1.19.33" || active != bin {
		t.Fatalf("在线内核未生效：%q %q", active, v)
	}
	if ver, src := p.MihomoVersion(); ver != "v1.19.33" || src != "online" {
		t.Fatalf("版本来源不正确：%q %q", ver, src)
	}
}

func TestCleanupKernels(t *testing.T) {
	p := &Paths{AppDest: t.TempDir(), Var: t.TempDir(), Arch: "x86_64"}
	versions := []string{"v1.19.30", "v1.19.31", "v1.19.32", "v1.19.33"}
	for i, v := range versions {
		bin := p.KernelBin(v)
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeKernelMeta(p, &KernelMeta{Version: v, Asset: linuxAssetName(v), InstalledAt: "2026-10-2" + string(rune('0'+i)) + " 00:00:00"}); err != nil {
			t.Fatal(err)
		}
	}
	cleanupKernels(p, "v1.19.33")
	if _, err := os.Stat(p.KernelBin("v1.19.33")); err != nil {
		t.Fatalf("当前版本不应被清理：%v", err)
	}
	left := installedKernels(p)
	if len(left) > 3 {
		t.Fatalf("应只保留当前 + 最近 2 个历史版本，实际 %d", len(left))
	}
}
