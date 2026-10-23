package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 在线切换 Mihomo 内核版本。
//
// 设计要点：
//   - 只接受 Mihomo 官方仓库（MetaCubeX/mihomo）的发行版，asset 名由本程序拼装，
//     不使用 API 返回的 browser_download_url，避免被镜像/中间人改写下载地址；
//   - 下载到应用私有目录 <var>/kernels/<tag>/mihomo（升级应用不会丢），
//     选中状态写入 <var>/kernels/state.json；未选中/文件缺失时自动回落到包内自带内核；
//   - 替换后先跑 "mihomo -v" 验证可执行且版本匹配，再重启内核；
//     重启失败自动回滚到原内核；
//   - 国内直连 GitHub 常失败，因此 API 与下载都走「直连 + 多个镜像 + 本机代理」多路尝试。

const (
	kernelRepo        = "MetaCubeX/mihomo"
	kernelAPIPageSize = 8 // 每个版本有 v1/v2/v3、go120/go123 等多个编译变体，少取几个版本避免列表过长
	kernelMaxGzSize   = 220 << 20 // 压缩包上限
	kernelMaxBinSize  = 300 << 20 // 解压后上限
	kernelCacheTTL    = 10 * time.Minute
)

// GitHub 官方 API + 常用加速镜像（镜像只是转发，返回内容仍是官方 JSON）。
var kernelAPIMirrors = []string{
	"https://api.github.com/repos/" + kernelRepo + "/releases?per_page=" + strconv.Itoa(kernelAPIPageSize),
	"https://ghfast.top/https://api.github.com/repos/" + kernelRepo + "/releases?per_page=" + strconv.Itoa(kernelAPIPageSize),
	"https://ghproxy.net/https://api.github.com/repos/" + kernelRepo + "/releases?per_page=" + strconv.Itoa(kernelAPIPageSize),
	"https://gh-proxy.com/https://api.github.com/repos/" + kernelRepo + "/releases?per_page=" + strconv.Itoa(kernelAPIPageSize),
}

// 下载地址模板：%s 为官方 releases/download 地址。
var kernelDownloadMirrors = []string{
	"%s",
	"https://ghfast.top/%s",
	"https://ghproxy.net/%s",
	"https://gh-proxy.com/%s",
}

// 只接受官方语义化版本 tag（例如 v1.19.33）；Prerelease-Alpha 之类直接忽略。
// 允许 tag 后缀带多个连字符，因为编译变体的目录标识形如 v1.19.33-v2-go123。
var kernelTagRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.-]+)?$`)

// 官方 asset 名白名单：mihomo-linux-<goarch>-<变体>-<tag>.gz，禁止斜杠/路径穿越字符。
var kernelAssetRE = regexp.MustCompile(`^mihomo-linux-[a-z0-9]+-[A-Za-z0-9][A-Za-z0-9._-]*\.gz$`)

// 从 "mihomo -v" 输出里提取版本号（输出形如 "Mihomo Meta v1.19.33 linux amd64 ..."）。
var kernelVersionOutputRE = regexp.MustCompile(`v\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.]+)?`)

// KernelState 记录当前选中的内核版本；Version 为空表示跟随随应用内置的内核。
type KernelState struct {
	Version   string `json:"version"`            // 内核目录标识（在线版为 tag 或 tag-变体，本地文件为 v0.0.0-local*）
	Label     string `json:"label,omitempty"`    // 展示用版本号（本地文件从 mihomo -v 识别）
	Source    string `json:"source,omitempty"`   // online / local / builtin
	UpdatedAt string `json:"updated_at,omitempty"`
}

// KernelMeta 记录某个已下载内核的来源与校验信息。
type KernelMeta struct {
	Version     string `json:"version"`
	Tag         string `json:"tag,omitempty"`     // 官方版本 tag（如 v1.19.33）
	Label       string `json:"label,omitempty"`   // 展示用完整版本（如 v2-go123-v1.19.33）
	Source      string `json:"source,omitempty"`  // online / local
	Asset       string `json:"asset"`             // 在线版为官方 asset 名；本地文件为原文件名
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`                 // 解压后可执行文件的 SHA256
	AssetSHA256 string `json:"asset_sha256,omitempty"` // 官方压缩包的 SHA256（校验镜像完整性）
	InstalledAt string `json:"installed_at"`
}

// KernelRelease 是设置页「切换版本」弹窗里展示的官方发行版。
type KernelRelease struct {
	Tag         string `json:"tag"`   // 官方版本 tag（用于下载地址）
	Label       string `json:"label"` // 展示格式：v2-go123-v1.19.33 / compatible-v1.19.33 / v1.19.33
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Asset       string `json:"asset"`
	Size        int64  `json:"size"`
	// AssetSHA256 是官方 .gz 压缩包的 SHA256（GitHub API 的 digest 字段），用于校验镜像下载内容。
	AssetSHA256 string `json:"asset_sha256,omitempty"`
	Downloaded  bool   `json:"downloaded"`
}

// InstalledKernel 是本地已下载的内核版本。
type InstalledKernel struct {
	Version     string `json:"version"`
	Tag         string `json:"tag,omitempty"`
	Label       string `json:"label,omitempty"`
	Source      string `json:"source,omitempty"`
	Asset       string `json:"asset,omitempty"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	InstalledAt string `json:"installed_at"`
	Active      bool   `json:"active"`
}

// KernelStatus 是下载/切换任务的实时状态（前端轮询）。
type KernelStatus struct {
	Busy       bool   `json:"busy"`
	Stage      string `json:"stage"` // idle / downloading / verifying / activating / done / error
	Version    string `json:"version"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Message    string `json:"message"`
	Error      string `json:"error"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt string    `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []ghAsset `json:"assets"`
}

const kernelReleaseCacheFormat = 2 // 2 = 按编译变体展开的列表；旧缓存自动失效

type kernelReleaseCache struct {
	FetchedAt int64           `json:"fetched_at"`
	Format    int             `json:"format"`
	Arch      string          `json:"arch"` // 缓存只对生成它的架构有效
	Releases  []KernelRelease `json:"releases"`
}

const (
	kernelAPIAttemptTimeout = 15 * time.Second
	kernelAPITotalBudget    = 100 * time.Second
	kernelDownloadBudget    = 45 * time.Minute
	kernelDownloadAttempt   = 25 * time.Minute
	kernelIdleTimeout       = 60 * time.Second
)

var kernelRelMu sync.Mutex

// ---------- 路径 / 状态 ----------

func (p *Paths) KernelDir() string       { return filepath.Join(p.Var, "kernels") }
func (p *Paths) KernelStateFile() string { return filepath.Join(p.KernelDir(), "state.json") }
func (p *Paths) KernelBin(version string) string {
	return filepath.Join(p.KernelDir(), version, "mihomo")
}

// BundledMihomoBin 返回安装包内自带的内核路径。
func (p *Paths) BundledMihomoBin() string { return filepath.Join(p.AppDest, "bin", p.Arch, "mihomo") }

func loadKernelState(p *Paths) *KernelState {
	b, err := os.ReadFile(p.KernelStateFile())
	if err != nil {
		return nil
	}
	var st KernelState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	if st.Version != "" && !kernelTagRE.MatchString(st.Version) {
		return nil
	}
	return &st
}

func saveKernelState(p *Paths, st *KernelState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p.KernelStateFile(), append(b, '\n'), 0o600)
}

// activeKernelBin 返回实际会被启动的内核路径：优先用户在线下载/本地导入的版本，否则包内自带。
func (p *Paths) activeKernelBin() (string, string) {
	if st := loadKernelState(p); st != nil && st.Version != "" {
		bin := p.KernelBin(st.Version)
		if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			m := readKernelMeta(p, st.Version)
			if m == nil {
				return p.BundledMihomoBin(), ""
			}
			// 在线版必须是当前架构的官方 .gz 包（含 v1/v2/v3、go 变体）；本地文件导入时已校验可执行。
			if m.Source == "local" || assetFitsArch(m.Asset) {
				return bin, st.Version
			}
		}
	}
	return p.BundledMihomoBin(), ""
}

func readKernelMeta(p *Paths, version string) *KernelMeta {
	b, err := os.ReadFile(filepath.Join(p.KernelDir(), version, "meta.json"))
	if err != nil {
		return nil
	}
	var m KernelMeta
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return &m
}

func writeKernelMeta(p *Paths, m *KernelMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(p.KernelDir(), m.Version, "meta.json"), append(b, '\n'), 0o644)
}

// installedKernels 列出 <var>/kernels 下已经下载完成的版本（最新安装的在前）。
func installedKernels(p *Paths) []InstalledKernel {
	active, _ := p.activeKernelBin()
	ents, err := os.ReadDir(p.KernelDir())
	if err != nil {
		return []InstalledKernel{}
	}
	out := []InstalledKernel{}
	for _, e := range ents {
		if !e.IsDir() || !kernelTagRE.MatchString(e.Name()) {
			continue
		}
		bin := p.KernelBin(e.Name())
		fi, err := os.Stat(bin)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		m := readKernelMeta(p, e.Name())
		if m == nil {
			continue // 没有元信息的目录不展示
		}
		if m.Source != "local" && !assetFitsArch(m.Asset) {
			continue // 在线版必须是当前架构的官方 .gz 包
		}
		it := InstalledKernel{Version: e.Name(), Tag: m.Tag, Asset: m.Asset, Size: fi.Size(), Active: bin == active}
		it.Label, it.Source = m.Label, m.Source
		if it.Label == "" {
			it.Label = e.Name()
		}
		if it.Source == "" {
			it.Source = "online"
		}
		it.SHA256, it.InstalledAt = m.SHA256, m.InstalledAt
		if m.Size > 0 {
			it.Size = m.Size
		}
		if it.InstalledAt == "" {
			it.InstalledAt = fi.ModTime().Format("2006-01-02 15:04:05")
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstalledAt > out[j].InstalledAt })
	return out
}

// cleanupKernels 只保留当前版本 + 最近 2 个历史版本，避免每个版本几十 MB 越堆越多。
func cleanupKernels(p *Paths, keep string) {
	ents, err := os.ReadDir(p.KernelDir())
	if err != nil {
		return
	}
	type cand struct {
		name string
		at   string
	}
	var dirs []cand
	now := time.Now()
	for _, e := range ents {
		if !e.IsDir() || !kernelTagRE.MatchString(e.Name()) || e.Name() == keep {
			continue
		}
		at := ""
		if m := readKernelMeta(p, e.Name()); m != nil {
			at = m.InstalledAt
		}
		if at == "" {
			if fi, err := e.Info(); err == nil {
				at = fi.ModTime().Format("2006-01-02 15:04:05")
			}
		}
		dirs = append(dirs, cand{name: e.Name(), at: at})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].at > dirs[j].at })
	for i, d := range dirs {
		if i < 2 {
			continue
		}
		_ = os.RemoveAll(filepath.Join(p.KernelDir(), d.name))
	}
	// 清理超过 1 小时的中断下载临时文件
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".download-") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(p.KernelDir(), e.Name()))
		}
	}
}

// ---------- 官方发行版列表 ----------

func linuxAssetPrefix() string {
	return "mihomo-linux-" + runtime.GOARCH + "-"
}

// linuxAssetName 返回当前架构的默认（无 CPU 变体后缀）官方 asset 名。
func linuxAssetName(tag string) string {
	return linuxAssetPrefix() + tag + ".gz"
}

func officialAssetURL(tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", kernelRepo, tag, asset)
}

// assetFitsArch 判断 asset 是否为当前架构的 .gz 内核包（amd64 的 v1/v2/v3、go120/go123、compatible 都算）。
func assetFitsArch(asset string) bool {
	return kernelAssetRE.MatchString(asset) && strings.HasPrefix(asset, linuxAssetPrefix())
}

// assetLabel 把官方 asset 名转成展示格式：
// mihomo-linux-amd64-v2-go123-v1.19.33.gz -> v2-go123-v1.19.33
func assetLabel(asset, tag string) string {
	s := strings.TrimPrefix(asset, linuxAssetPrefix())
	return strings.TrimSuffix(s, ".gz")
}

// variantSuffix 从 asset 里取出编译变体后缀：v2-go123 / v1 / compatible / ""。
func variantSuffix(asset, tag string) string {
	s := assetLabel(asset, tag)
	s = strings.TrimSuffix(s, "-"+tag)
	s = strings.TrimSuffix(s, tag)
	return strings.Trim(s, "-.")
}

// variantKey 生成内核目录/状态用的唯一标识：v1.19.33-v2-go123。
func variantKey(asset, tag string) string {
	if s := variantSuffix(asset, tag); s != "" {
		return tag + "-" + s
	}
	return tag
}

var kernelVariantRE = regexp.MustCompile(`^v([123])(?:-go(\d+))?$`)

// variantRank 只用于同一版本内部的排序：默认包 → compatible → v1/v2/v3 → 各 go 变体。
func variantRank(asset, tag string) int {
	s := variantSuffix(asset, tag)
	if s == "" {
		return 0
	}
	if s == "compatible" {
		return 1
	}
	if m := kernelVariantRE.FindStringSubmatch(s); m != nil {
		rank := 10 + int(m[1][0]-'0')*10
		if m[2] != "" {
			rank++
		}
		return rank
	}
	return 100
}

func newKernelHTTPClient(timeout time.Duration, proxyAddr string) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: 25 * time.Second,
		TLSHandshakeTimeout:   12 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	if proxyAddr != "" {
		if pu, err := url.Parse("http://" + proxyAddr); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func (a *App) kernelHTTPClients(timeout time.Duration) []*http.Client {
	clients := []*http.Client{}
	// 内核运行中时优先走本机代理：应用本身就是代理，国内直连 GitHub 往往失败，
	// 而走当前订阅节点通常能稳定访问；代理不可用时再退回直连 + 加速镜像。
	if a.Mihomo.Running() {
		s := loadSettings()
		clients = append(clients, newKernelHTTPClient(timeout, "127.0.0.1:"+strconv.Itoa(s.ProxyPort)))
	}
	clients = append(clients, newKernelHTTPClient(timeout, ""))
	return clients
}

// listKernelReleases 拉取官方 Releases（失败时回落本地缓存），结果缓存 10 分钟。
// 返回值：发行版列表、警告（使用缓存时说明原因）、缓存时间、错误。
func (a *App) listKernelReleases(force bool) ([]KernelRelease, string, int64, error) {
	kernelRelMu.Lock()
	defer kernelRelMu.Unlock()

	cachePath := filepath.Join(a.Paths.KernelDir(), "releases.json")
	var cached *kernelReleaseCache
	if b, err := os.ReadFile(cachePath); err == nil {
		var c kernelReleaseCache
		// 旧缓存没有 arch 字段，或来自另一种 CPU 架构时直接忽略，避免列出本机装不了的内核。
		if json.Unmarshal(b, &c) == nil && len(c.Releases) > 0 && c.Format == kernelReleaseCacheFormat && c.Arch == runtime.GOARCH {
			c.Releases = filterReleasesForArch(c.Releases)
			if len(c.Releases) > 0 {
				cached = &c
			}
		}
	}
	if !force && cached != nil && time.Since(time.Unix(cached.FetchedAt, 0)) < kernelCacheTTL {
		return cached.Releases, "", cached.FetchedAt, nil
	}

	var errs []string
	budget := time.Now().Add(kernelAPITotalBudget)
	for _, cl := range a.kernelHTTPClients(30 * time.Second) {
		for _, u := range kernelAPIMirrors {
			if time.Now().After(budget) {
				errs = append(errs, "已超出获取版本列表的时限")
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), kernelAPIAttemptTimeout)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				cancel()
				continue
			}
			req.Header.Set("User-Agent", "MihomoProxy-kernel")
			req.Header.Set("Accept", "application/vnd.github+json")
			resp, err := cl.Do(req)
			if err != nil {
				cancel()
				errs = append(errs, shortErr(u, err))
				continue
			}
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
			resp.Body.Close()
			cancel()
			if rerr != nil {
				errs = append(errs, hostOf(u)+": "+rerr.Error())
				continue
			}
			if resp.StatusCode != http.StatusOK {
				errs = append(errs, fmt.Sprintf("%s: HTTP %d", hostOf(u), resp.StatusCode))
				continue
			}
			var raw []ghRelease
			if err := json.Unmarshal(body, &raw); err != nil {
				errs = append(errs, hostOf(u)+": 返回内容不是合法的发布列表")
				continue
			}
			list := filterKernelReleases(raw)
			if len(list) == 0 {
				errs = append(errs, fmt.Sprintf("%s: 没有找到适用于 linux/%s 的发行版", hostOf(u), runtime.GOARCH))
				continue
			}
			c := kernelReleaseCache{FetchedAt: time.Now().Unix(), Format: kernelReleaseCacheFormat, Arch: runtime.GOARCH, Releases: list}
			if b, err := json.MarshalIndent(c, "", "  "); err == nil {
				_ = atomicWrite(cachePath, append(b, '\n'), 0o644)
			}
			return list, "", c.FetchedAt, nil
		}
	}

	// 网络失败但本地有缓存：继续可用（设置页会提示列表可能不是最新）。
	if cached != nil {
		msg := "获取最新版本列表失败，当前显示的是缓存（" + time.Unix(cached.FetchedAt, 0).Format("2006-01-02 15:04") + "）"
		if len(errs) > 0 {
			msg += "：" + strings.Join(dedupe(errs, 4), "；")
		}
		return cached.Releases, msg, cached.FetchedAt, nil
	}
	if len(errs) == 0 {
		errs = []string{"没有可用的下载地址"}
	}
	return nil, "", 0, fmt.Errorf("获取 Mihomo 官方版本列表失败：%s", strings.Join(dedupe(errs, 6), "；"))
}

// normalizeSHA 把 GitHub API 的 digest（"sha256:..."）规范成 64 位小写十六进制；非法返回空。
func normalizeSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

// releaseFitsArch 判断发行版条目是否为当前 CPU 架构的官方 .gz 内核包。
func releaseFitsArch(rel KernelRelease) bool {
	return assetFitsArch(rel.Asset)
}

// filterReleasesForArch 只保留当前架构可下载的版本（缓存 / 接口返回前再兜一层）。
func filterReleasesForArch(list []KernelRelease) []KernelRelease {
	out := make([]KernelRelease, 0, len(list))
	for _, rel := range list {
		if releaseFitsArch(rel) {
			out = append(out, rel)
		}
	}
	return out
}

// filterKernelReleases 把每个官方 Release 展开成多个编译变体：
// v1.19.33、compatible-v1.19.33、v1-v1.19.33、v2-go123-v1.19.33 ...
func filterKernelReleases(raw []ghRelease) []KernelRelease {
	out := []KernelRelease{}
	for _, r := range raw {
		if r.Draft || !kernelTagRE.MatchString(r.TagName) {
			continue
		}
		group := []KernelRelease{}
		for _, asset := range r.Assets {
			if !assetFitsArch(asset.Name) || !strings.HasSuffix(asset.Name, "-"+r.TagName+".gz") {
				continue
			}
			group = append(group, KernelRelease{
				Tag:         r.TagName,
				Label:       assetLabel(asset.Name, r.TagName),
				PublishedAt: r.PublishedAt,
				Prerelease:  r.Prerelease,
				Asset:       asset.Name,
				Size:        asset.Size,
				AssetSHA256: normalizeSHA(asset.Digest),
			})
		}
		sort.SliceStable(group, func(i, j int) bool {
			ri, rj := variantRank(group[i].Asset, group[i].Tag), variantRank(group[j].Asset, group[j].Tag)
			if ri != rj {
				return ri < rj
			}
			return group[i].Label < group[j].Label
		})
		out = append(out, group...)
	}
	return out
}

// ---------- 下载 / 校验 ----------

// idleCancelReader 在连续 timeout 时间内没有收到任何字节时取消请求。
// 这样「慢但一直在传」的镜像可以继续下完，而「连上了却不发数据」的线路会很快失败。
type idleCancelReader struct {
	r       io.Reader
	timer   *time.Timer
	timeout time.Duration
	idle    int32
}

func newIdleCancelReader(r io.Reader, timeout time.Duration, cancel context.CancelFunc) *idleCancelReader {
	ir := &idleCancelReader{r: r, timeout: timeout}
	ir.timer = time.AfterFunc(timeout, func() {
		atomic.StoreInt32(&ir.idle, 1)
		cancel()
	})
	return ir
}

func (ir *idleCancelReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.timer.Reset(ir.timeout)
	}
	return n, err
}

func (ir *idleCancelReader) stop() { ir.timer.Stop() }

func (ir *idleCancelReader) idleHit() bool { return atomic.LoadInt32(&ir.idle) == 1 }

type kernelProgressReader struct {
	r     io.Reader
	a     *App
	local int64
}

func (p *kernelProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.local += int64(n)
		if p.local >= 256<<10 {
			p.a.kernelAddProgress(p.local)
			p.local = 0
		}
	}
	return n, err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// probeKernelBinary 确认是 Linux 可执行文件，并返回 "mihomo -v" 识别出的版本号。
func probeKernelBinary(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	magic := make([]byte, 4)
	_, rerr := io.ReadFull(f, magic)
	f.Close()
	if rerr != nil || !bytes.Equal(magic, []byte{0x7f, 'E', 'L', 'F'}) {
		return "", fmt.Errorf("文件不是 Linux 可执行内核")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, runErr := exec.CommandContext(ctx, path, "-v").CombinedOutput()
	text := string(out)
	if v := kernelVersionOutputRE.FindString(text); v != "" {
		return v, nil
	}
	short := strings.TrimSpace(text)
	if len(short) > 200 {
		short = short[:200] + "…"
	}
	if short == "" {
		if runErr != nil {
			short = runErr.Error()
		} else {
			short = "无输出"
		}
	}
	return "", fmt.Errorf("内核自检失败：无法从 mihomo -v 输出识别版本（%s）", short)
}

// verifyKernelBinary 确认下载/导入的内核版本与期望版本一致。
func verifyKernelBinary(path, version string) error {
	got, err := probeKernelBinary(path)
	if err != nil {
		return err
	}
	if got != version {
		return fmt.Errorf("内核自检失败（期望 %s，实际 %s）", version, got)
	}
	return nil
}

// downloadKernel 依次尝试官方地址与各镜像，成功返回解压后的临时文件路径。
// key 是内核目录标识（tag 或 tag-变体），tag 用于拼官方下载地址，asset 是选中的编译变体。
func (a *App) downloadKernel(key, tag, asset string, size int64, wantSHA string) (string, error) {
	dir := filepath.Join(a.Paths.KernelDir(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	official := officialAssetURL(tag, asset)
	budget := time.Now().Add(kernelDownloadBudget)
	var errs []string
	clients := a.kernelHTTPClients(60 * time.Minute)
	// 外层镜像、内层客户端：同一个镜像先走本机代理再走直连，
	// 避免代理整条线路不可用时把时间全耗在代理上、迟迟轮不到直连。
	for _, tpl := range kernelDownloadMirrors {
		for _, cl := range clients {
			if time.Now().After(budget) {
				errs = append(errs, "已超出下载时限")
				break
			}
			u := fmt.Sprintf(tpl, official)
			a.kernelSet(func(s *KernelStatus) { s.Message = "正在从 " + hostOf(u) + " 下载…" })
			path, err := a.downloadOnce(cl, u, key, size, wantSHA)
			if err == nil {
				return path, nil
			}
			errs = append(errs, shortErr(u, err))
		}
	}
	if len(errs) == 0 {
		errs = []string{"没有可用的下载地址"}
	}
	return "", fmt.Errorf("内核下载失败：%s", strings.Join(dedupe(errs, 6), "；"))
}

func (a *App) downloadOnce(cl *http.Client, u, key string, size int64, wantSHA string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), kernelDownloadAttempt)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "MihomoProxy-kernel")
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if size <= 0 && resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	a.kernelSet(func(s *KernelStatus) {
		s.Total = size
		s.Downloaded = 0
	})
	body := newIdleCancelReader(resp.Body, kernelIdleTimeout, cancel)
	defer body.stop()
	hash := sha256.New()
	raw := io.TeeReader(&kernelProgressReader{r: io.LimitReader(body, kernelMaxGzSize), a: a}, hash)
	gz, err := gzip.NewReader(raw)
	if err != nil {
		if body.idleHit() {
			return "", fmt.Errorf("下载停滞超过 %d 秒", int(kernelIdleTimeout/time.Second))
		}
		return "", fmt.Errorf("压缩包无效：%v", err)
	}
	defer gz.Close()

	f, err := os.CreateTemp(filepath.Dir(filepath.Join(a.Paths.KernelDir(), key, "mihomo")), ".download-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()

	n, err := io.Copy(f, io.LimitReader(gz, kernelMaxBinSize+1))
	if err != nil {
		if body.idleHit() {
			return "", fmt.Errorf("下载停滞超过 %d 秒", int(kernelIdleTimeout/time.Second))
		}
		return "", err
	}
	if n > kernelMaxBinSize {
		return "", fmt.Errorf("解压后超过大小上限")
	}
	if wantSHA != "" {
		got := hex.EncodeToString(hash.Sum(nil))
		if got != wantSHA {
			return "", fmt.Errorf("官方 SHA256 校验失败（期望 %s…，实际 %s…）", wantSHA[:12], got[:12])
		}
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	ok = true
	return tmp, nil
}

// ensureKernelVariantInstalled 保证 <var>/kernels/<key>/mihomo 存在且可用；已存在则跳过下载。
// key 是目录唯一标识（如 v1.19.33-v2-go123），tag 是官方版本，asset 是具体编译变体。
func (a *App) ensureKernelVariantInstalled(key, tag, asset, label string, size int64, wantSHA string) error {
	bin := a.Paths.KernelBin(key)
	if fi, err := os.Stat(bin); err == nil && fi.Mode().IsRegular() && fi.Size() > 1<<20 {
		a.kernelSet(func(s *KernelStatus) {
			s.Stage = "verifying"
			s.Message = "本地已有该版本，正在校验…"
		})
		meta := readKernelMeta(a.Paths, key)
		// 必须同一个编译变体；有官方摘要时还要求摘要一致。
		trusted := meta != nil && meta.Asset == asset && (wantSHA == "" || meta.AssetSHA256 == wantSHA)
		if trusted && verifyKernelBinary(bin, tag) == nil {
			return nil
		}
	}

	tmp, err := a.downloadKernel(key, tag, asset, size, wantSHA)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	a.kernelSet(func(s *KernelStatus) {
		s.Stage = "verifying"
		s.Message = "下载完成，正在校验内核…"
	})
	if err := verifyKernelBinary(tmp, tag); err != nil {
		return err
	}
	sha, err := sha256File(tmp)
	if err != nil {
		return err
	}
	dst := a.Paths.KernelBin(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	// 直接 rename 覆盖旧文件（同一目录同一文件系统），失败时旧文件仍然可用。
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	fi, _ := os.Stat(dst)
	var sz int64
	if fi != nil {
		sz = fi.Size()
	}
	return writeKernelMeta(a.Paths, &KernelMeta{
		Version:     key,
		Tag:         tag,
		Label:       label,
		Source:      "online",
		Asset:       asset,
		Size:        sz,
		SHA256:      sha,
		AssetSHA256: wantSHA,
		InstalledAt: nowStr(),
	})
}

// ---------- 本地文件内核（飞牛文件选择器） ----------

// openUserPickedKernelFile 校验并打开用户通过飞牛文件选择器授权的内核文件。
// pickUserFile 已经把所选文件的访问权限授给当前应用，所以这里不再限制固定目录，
// 只做绝对路径 / 常规文件 / 大小校验；能不能读由宿主授权和文件系统权限决定。
func openUserPickedKernelFile(p string) (*os.File, string, os.FileInfo, error) {
	if p == "" || !strings.HasPrefix(p, "/") {
		return nil, "", nil, fmt.Errorf("文件路径无效")
	}
	clean := path.Clean(p)
	if clean == "/" {
		return nil, "", nil, fmt.Errorf("文件路径无效")
	}
	st, err := os.Stat(clean)
	if err != nil {
		return nil, "", nil, fmt.Errorf("无法读取文件：%v", err)
	}
	if !st.Mode().IsRegular() {
		return nil, "", nil, fmt.Errorf("不是常规文件")
	}
	if st.Size() <= 0 || st.Size() > kernelMaxBinSize {
		return nil, "", nil, fmt.Errorf("文件大小超出范围（需小于 %d MB）", kernelMaxBinSize>>20)
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, "", nil, fmt.Errorf("无法打开文件：%v", err)
	}
	return f, clean, st, nil
}

func localKernelKey() string {
	// 必须匹配 kernelTagRE（后缀里不能再出现 "-"），同时保证多次导入不会撞目录。
	return fmt.Sprintf("v0.0.0-local%d", time.Now().UnixNano())
}

// runKernelFileSwitch 从用户选择的文件安装内核：支持官方 .gz 压缩包或已解压的 mihomo 二进制。
func (a *App) runKernelFileSwitch(src string) {
	defer func() {
		if r := recover(); r != nil {
			a.kernelFail(fmt.Errorf("本地内核导入异常：%v", r))
		}
	}()

	f, clean, st, err := openUserPickedKernelFile(src)
	if err != nil {
		a.kernelFail(err)
		return
	}
	defer f.Close()

	key := localKernelKey()
	dir := filepath.Join(a.Paths.KernelDir(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.kernelFail(err)
		return
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()

	a.kernelSet(func(s *KernelStatus) {
		s.Stage = "verifying"
		s.Message = "正在读取本地内核…"
		s.Total = st.Size()
		s.Downloaded = 0
	})

	tmp, err := os.CreateTemp(dir, ".local-*")
	if err != nil {
		a.kernelFail(err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	br := bufio.NewReader(f)
	magic, _ := br.Peek(2)
	var reader io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gerr := gzip.NewReader(&kernelProgressReader{r: br, a: a})
		if gerr != nil {
			tmp.Close()
			a.kernelFail(fmt.Errorf("压缩包无效：%v", gerr))
			return
		}
		defer gz.Close()
		reader = gz
	} else {
		reader = &kernelProgressReader{r: br, a: a}
	}

	n, err := io.Copy(tmp, io.LimitReader(reader, kernelMaxBinSize+1))
	if err != nil {
		tmp.Close()
		a.kernelFail(err)
		return
	}
	if n > kernelMaxBinSize {
		tmp.Close()
		a.kernelFail(fmt.Errorf("解压后超过大小上限"))
		return
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		a.kernelFail(err)
		return
	}
	if err := tmp.Close(); err != nil {
		a.kernelFail(err)
		return
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		a.kernelFail(err)
		return
	}

	a.kernelSet(func(s *KernelStatus) {
		s.Stage = "verifying"
		s.Message = "正在校验并识别本地内核版本…"
	})
	detected, err := probeKernelBinary(tmpName)
	if err != nil {
		a.kernelFail(fmt.Errorf("本地内核自检失败：%v（请确认它与 NAS 架构 linux/%s 一致）", err, runtime.GOARCH))
		return
	}
	sha, err := sha256File(tmpName)
	if err != nil {
		a.kernelFail(err)
		return
	}
	dst := a.Paths.KernelBin(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		a.kernelFail(err)
		return
	}
	if err := os.Rename(tmpName, dst); err != nil {
		a.kernelFail(err)
		return
	}
	fi, _ := os.Stat(dst)
	var sz int64
	if fi != nil {
		sz = fi.Size()
	}
	if err := writeKernelMeta(a.Paths, &KernelMeta{
		Version:     key,
		Label:       detected,
		Source:      "local",
		Asset:       filepath.Base(clean),
		Size:        sz,
		SHA256:      sha,
		InstalledAt: nowStr(),
	}); err != nil {
		a.kernelFail(err)
		return
	}
	ok = true

	a.kernelSet(func(s *KernelStatus) {
		s.Stage = "activating"
		s.Message = "正在切换并重启内核…"
	})
	if err := a.activateKernel(key, detected, "local"); err != nil {
		a.kernelFail(err)
		return
	}
	cleanupKernels(a.Paths, key)
	a.kernelDone(detected)
}

// StartKernelFileSwitch 异步安装并启用用户从飞牛文件选择器挑的内核文件。
func (a *App) StartKernelFileSwitch(p string) error {
	f, _, _, err := openUserPickedKernelFile(p)
	if err != nil {
		return err
	}
	f.Close()
	a.kMu.Lock()
	if a.kSt.Busy {
		a.kMu.Unlock()
		return fmt.Errorf("已有内核切换任务正在进行中，请稍候")
	}
	a.kSt = KernelStatus{
		Busy:    true,
		Stage:   "queued",
		Message: "正在读取本地内核…",
	}
	a.kSt.StartedAt = nowUnix()
	a.kMu.Unlock()
	go a.runKernelFileSwitch(p)
	return nil
}

// ---------- 激活 / 回滚 ----------

// activateKernel 写入选中版本；内核运行中则先停后启，新内核起不来就回滚旧版本。
func (a *App) activateKernel(version, label, source string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	old := &KernelState{}
	if cur := loadKernelState(a.Paths); cur != nil {
		old = &KernelState{Version: cur.Version, Label: cur.Label, Source: cur.Source}
	}
	wasRunning := a.Mihomo.Running()
	if wasRunning {
		if err := a.Mihomo.Stop(); err != nil {
			return fmt.Errorf("停止内核失败：%w", err)
		}
	}
	newState := &KernelState{Version: version, Label: label, Source: source, UpdatedAt: nowStr()}
	if err := saveKernelState(a.Paths, newState); err != nil {
		if wasRunning {
			_ = a.Mihomo.Start(a.Paths.ConfigFile())
		}
		return fmt.Errorf("保存内核选择失败：%w", err)
	}
	invalidateMihomoVersion()
	if !wasRunning {
		return nil
	}
	if err := a.Mihomo.Start(a.Paths.ConfigFile()); err != nil {
		old.UpdatedAt = nowStr()
		_ = saveKernelState(a.Paths, old)
		invalidateMihomoVersion()
		if e2 := a.Mihomo.Start(a.Paths.ConfigFile()); e2 != nil {
			return fmt.Errorf("新内核启动失败（%v），已回滚但原内核也未能启动：%v", err, e2)
		}
		return fmt.Errorf("新内核启动失败，已回滚到原版本：%v", err)
	}
	return nil
}

// ---------- 任务状态 ----------

func (a *App) kernelSet(fn func(*KernelStatus)) {
	a.kMu.Lock()
	defer a.kMu.Unlock()
	fn(&a.kSt)
}

func (a *App) kernelAddProgress(n int64) {
	a.kMu.Lock()
	defer a.kMu.Unlock()
	a.kSt.Downloaded += n
}

func (a *App) KernelStatus() KernelStatus {
	a.kMu.Lock()
	defer a.kMu.Unlock()
	return a.kSt
}

func (a *App) kernelFail(err error) {
	a.kernelSet(func(s *KernelStatus) {
		s.Busy = false
		s.Stage = "error"
		s.Error = err.Error()
		s.Message = "切换失败"
		s.FinishedAt = nowUnix()
	})
}

func (a *App) kernelDone(version string) {
	a.kernelSet(func(s *KernelStatus) {
		s.Busy = false
		s.Stage = "done"
		s.Version = version
		s.Message = "已切换到 " + version
		s.Error = ""
		s.FinishedAt = nowUnix()
	})
}

// StartKernelSwitch 异步下载并切换内核；version 为空表示恢复随应用内置的内核。
// asset 为空时使用当前架构的默认包，指定 asset 可切换 v1/v2/v3、go120/go123 等编译变体。
func (a *App) StartKernelSwitch(version, asset string) error {
	a.kMu.Lock()
	if a.kSt.Busy {
		a.kMu.Unlock()
		return fmt.Errorf("已有内核切换任务正在进行中，请稍候")
	}
	a.kSt = KernelStatus{
		Busy:      true,
		Stage:     "queued",
		Version:   version,
		Message:   "正在准备…",
		StartedAt: nowUnix(),
	}
	a.kMu.Unlock()
	go a.runKernelSwitch(version, asset)
	return nil
}

func (a *App) runKernelSwitch(version, asset string) {
	defer func() {
		if r := recover(); r != nil {
			a.kernelFail(fmt.Errorf("内核切换异常：%v", r))
		}
	}()

	key, label, source := "", "", "builtin"
	if version != "" {
		if asset == "" {
			asset = linuxAssetName(version)
		}
		label = assetLabel(asset, version)
		if label == "" {
			label = version
		}
		key = variantKey(asset, version)
		source = "online"

		// 拿一次发行版信息以便显示下载大小、并取得官方 SHA256；列表获取失败不阻塞
		//（下载地址由官方仓库 + tag 自行拼装，手动输入版本号也能用，只是没有摘要校验）。
		var size int64
		var wantSHA string
		if releases, _, _, err := a.listKernelReleases(false); err == nil {
			for _, r := range releases {
				if r.Asset == asset {
					size = r.Size
					wantSHA = r.AssetSHA256
					label = r.Label
					break
				}
			}
		}
		if err := a.ensureKernelVariantInstalled(key, version, asset, label, size, wantSHA); err != nil {
			a.kernelFail(err)
			return
		}
	}

	a.kernelSet(func(s *KernelStatus) {
		s.Stage = "activating"
		s.Message = "正在切换并重启内核…"
	})
	if err := a.activateKernel(key, label, source); err != nil {
		a.kernelFail(err)
		return
	}
	cleanupKernels(a.Paths, key)
	if source == "builtin" {
		label = mihomoBundled
		if label == "" {
			label = "随应用内置"
		}
	}
	a.kernelDone(label)
}

// ---------- HTTP 接口 ----------

func (a *App) hMihomoKernel(w http.ResponseWriter, r *http.Request) {
	active, source := a.Paths.MihomoVersion()
	writeJSON(w, 200, map[string]any{
		"active":    active,
		"source":    source,
		"bundled":   mihomoBundled,
		"arch":      a.Paths.Arch,
		"goarch":    runtime.GOARCH,
		"dir":       a.Paths.KernelDir(),
		"installed": installedKernels(a.Paths),
		"status":    a.KernelStatus(),
	})
}

func (a *App) hMihomoReleases(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	releases, warning, fetchedAt, err := a.listKernelReleases(force)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	// 双重保险：只返回当前架构能装的官方内核（缓存/镜像返回异常时也不会列错）。
	releases = filterReleasesForArch(releases)
	have := map[string]bool{}
	for _, it := range installedKernels(a.Paths) {
		if it.Asset != "" {
			have[it.Asset] = true
		}
	}
	for i := range releases {
		releases[i].Downloaded = have[releases[i].Asset]
	}
	active, source := a.Paths.MihomoVersion()
	writeJSON(w, 200, map[string]any{
		"releases":   releases,
		"warning":    warning,
		"fetched_at": fetchedAt,
		"active":     active,
		"source":     source,
		"bundled":    mihomoBundled,
		"arch":       a.Paths.Arch,
		"goarch":     runtime.GOARCH,
	})
}

func (a *App) hMihomoSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
		Asset   string `json:"asset"`
		Path    string `json:"path"`
		Builtin bool   `json:"builtin"`
	}
	b, _ := readBody(r, 1<<16)
	if err := json.Unmarshal(b, &body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	version := strings.TrimSpace(body.Version)
	asset := strings.TrimSpace(body.Asset)
	if body.Builtin {
		version, asset = "", ""
	}
	// 飞牛文件选择器挑的本地内核文件（官方 .gz 或已解压二进制）。
	if p := strings.TrimSpace(body.Path); p != "" {
		if version != "" || asset != "" || body.Builtin {
			writeErr(w, 400, "path 不能与 version/asset/builtin 同时使用")
			return
		}
		if err := a.StartKernelFileSwitch(p); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 202, map[string]any{"ok": true, "status": a.KernelStatus()})
		return
	}
	if version != "" && !kernelTagRE.MatchString(version) {
		writeErr(w, 400, "版本号格式不正确（只支持 Mihomo 官方 vX.Y.Z 版本）")
		return
	}
	if asset != "" {
		if version == "" {
			writeErr(w, 400, "指定 asset 时必须同时提供 version")
			return
		}
		if !assetFitsArch(asset) || !strings.HasSuffix(asset, "-"+version+".gz") {
			writeErr(w, 400, "asset 必须是当前架构、且属于该版本的官方 .gz 内核包")
			return
		}
	}
	if version == "" && !body.Builtin {
		writeErr(w, 400, "缺少 version（或 builtin=true 恢复内置内核）")
		return
	}
	if err := a.StartKernelSwitch(version, asset); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "status": a.KernelStatus()})
}
