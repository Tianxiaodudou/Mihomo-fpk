package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 分流规则集的上游来源 + 备用镜像。
// 国内直连 raw.githubusercontent.com 常失败，所以准备多个镜像，任一可用即可。
const rulesUpstreamRepo = "Loyalsoldier/clash-rules"

var rulesMirrors = []string{
	"https://cdn.jsdelivr.net/gh/" + rulesUpstreamRepo + "@release/%s",
	"https://ghfast.top/https://raw.githubusercontent.com/" + rulesUpstreamRepo + "/release/%s",
	"https://ghproxy.net/https://raw.githubusercontent.com/" + rulesUpstreamRepo + "/release/%s",
	"https://raw.githubusercontent.com/" + rulesUpstreamRepo + "/release/%s",
}

// 下载参数：单次请求超时 / 整轮预算 / 每个镜像的重试轮数。
const (
	rulesAttemptTimeout = 25 * time.Second
	rulesTotalBudget    = 150 * time.Second
	rulesAttemptPasses  = 2
)

// rulesBuiltAt 由构建时注入（tools/build.py 的 -ldflags -X main.rulesBuiltAt=YYYY-MM-DD），
// 表示二进制里内置规则快照的日期，作为「规则版本」在未手动更新时的取值。
var rulesBuiltAt = ""

// RulesMeta 记录 <var>/rules 里规则集的来源与版本（手动更新后写入）。
type RulesMeta struct {
	UpdatedAt string         `json:"updated_at"` // RFC3339
	Source    string         `json:"source"`     // 上游仓库
	Origin    string         `json:"origin"`     // online | builtin
	Files     map[string]int `json:"files"`      // 规则集名 -> 条数
}

func rulesMetaFile(ruleDir string) string { return filepath.Join(ruleDir, "meta.json") }

func rulesDir(varDir string) string { return filepath.Join(varDir, "rules") }

// loadOnlineMeta 只有在 meta 有效且 5 个规则文件都存在时才返回（否则视为需要回落到内置）。
func loadOnlineMeta(ruleDir string) *RulesMeta {
	b, err := os.ReadFile(rulesMetaFile(ruleDir))
	if err != nil {
		return nil
	}
	var m RulesMeta
	if err := json.Unmarshal(b, &m); err != nil || m.Origin != "online" {
		return nil
	}
	for _, rp := range ruleProviderDefs {
		if _, err := os.Stat(filepath.Join(ruleDir, rp.Name+".yaml")); err != nil {
			return nil
		}
	}
	return &m
}

// onlineRulesActive 表示当前生效的是用户手动更新过的规则（不应被内置快照覆盖）。
func onlineRulesActive(ruleDir string) bool { return loadOnlineMeta(ruleDir) != nil }

// countRuleLines 统计 rule-provider（首行 payload:）的规则条数，口径与 tools/build.py 一致。
func countRuleLines(b []byte) int {
	n := strings.Count(string(b), "\n") - 1
	if n < 0 {
		n = 0
	}
	return n
}

func embeddedRulesCounts() map[string]int {
	out := map[string]int{}
	for _, rp := range ruleProviderDefs {
		if b, err := ruleFS.ReadFile(rp.File); err == nil {
			out[rp.Name] = countRuleLines(b)
		}
	}
	return out
}

func sumCounts(m map[string]int) int {
	t := 0
	for _, v := range m {
		t += v
	}
	return t
}

func rulesVersionLabel(meta *RulesMeta) string {
	if meta == nil {
		return rulesBuiltAt
	}
	if len(meta.UpdatedAt) >= 10 {
		return meta.UpdatedAt[:10]
	}
	return meta.UpdatedAt
}

// RulesInfo 汇总当前生效的规则集版本信息，供设置页「版本信息 / 分流规则」展示。
func RulesInfo(varDir string) map[string]any {
	dir := rulesDir(varDir)
	if m := loadOnlineMeta(dir); m != nil {
		return map[string]any{
			"version":    rulesVersionLabel(m),
			"origin":     "online",
			"updated_at": m.UpdatedAt,
			"source":     m.Source,
			"files":      m.Files,
			"total":      sumCounts(m.Files),
			"builtin":    rulesBuiltAt,
		}
	}
	files := embeddedRulesCounts()
	return map[string]any{
		"version":    rulesBuiltAt,
		"origin":     "builtin",
		"updated_at": "",
		"source":     rulesUpstreamRepo,
		"files":      files,
		"total":      sumCounts(files),
		"builtin":    rulesBuiltAt,
	}
}

// RulesVersion 是 /api/version 里用的短标签。
func RulesVersion(varDir string) (version, origin string) {
	if m := loadOnlineMeta(rulesDir(varDir)); m != nil {
		return rulesVersionLabel(m), "online"
	}
	return rulesBuiltAt, "builtin"
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// shortErr 把网络错误压成「主机名: 简短原因」，便于直接在设置页展示。
func shortErr(u string, err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "Get \""+u+"\": ", "")
	if strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "context deadline exceeded") {
		msg = "超时"
	}
	if len(msg) > 80 {
		msg = msg[:80] + "…"
	}
	return hostOf(u) + ": " + msg
}

// dedupe 去重并限制条数，避免错误信息过长。
func dedupe(msgs []string, limit int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range msgs {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// fetchRuleOnce 依镜像顺序尝试一轮，返回首个合法响应；
// 全部失败时返回每个镜像的错误摘要（而不是只有最后一个），便于定位真正原因。
func fetchRuleOnce(client *http.Client, fname string, budget time.Time) ([]byte, []string) {
	var errs []string
	for _, tpl := range rulesMirrors {
		if time.Now().After(budget) {
			errs = append(errs, "已超出本轮更新时限")
			break
		}
		u := fmt.Sprintf(tpl, fname)
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			errs = append(errs, hostOf(u)+": "+err.Error())
			continue
		}
		req.Header.Set("User-Agent", "MihomoProxy-rules")
		ctx, cancel := context.WithTimeout(context.Background(), rulesAttemptTimeout)
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			cancel()
			errs = append(errs, shortErr(u, err))
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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
		if !strings.HasPrefix(strings.TrimSpace(string(b)), "payload:") || len(b) < 200 {
			errs = append(errs, fmt.Sprintf("%s: 返回的不是合法规则集（%d 字节）", hostOf(u), len(b)))
			continue
		}
		return b, nil
	}
	return nil, errs
}

// fetchRule 先用直连客户端多轮尝试，再走本机代理多轮尝试（应用本身就是代理，GitHub 通常可达）。
func fetchRule(fname string, clients []*http.Client, budget time.Time) ([]byte, error) {
	var errs []string
	for _, cl := range clients {
		if cl == nil {
			continue
		}
		for pass := 0; pass < rulesAttemptPasses; pass++ {
			if time.Now().After(budget) {
				break
			}
			b, es := fetchRuleOnce(cl, fname, budget)
			if b != nil {
				return b, nil
			}
			errs = append(errs, es...)
		}
	}
	if len(errs) == 0 {
		errs = []string{"没有可用的下载地址"}
	}
	msg := strings.Join(dedupe(errs, 6), "；")
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	return nil, fmt.Errorf("%s", msg)
}

// proxyClient 直连失败时改走本机代理（应用本身就是代理，github 通常可达）。
func proxyClient(addr string) *http.Client {
	if addr == "" {
		return nil
	}
	pu, err := url.Parse("http://" + addr)
	if err != nil {
		return nil
	}
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
	}
}

// UpdateRules 拉取全部规则集写入 <var>/rules/ 并记录 meta。
// 全部拉取成功后才落盘，避免出现「一半新一半旧」的分流状态。
func UpdateRules(varDir, proxyAddr string) (*RulesMeta, error) {
	dir := rulesDir(varDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 直连多镜像 + 本机代理兜底；5 个规则集彼此独立，并行拉取缩短等待。
	clients := []*http.Client{{Timeout: 30 * time.Second}, proxyClient(proxyAddr)}
	budget := time.Now().Add(rulesTotalBudget)

	type fetchResult struct {
		b   []byte
		err error
	}
	results := make([]fetchResult, len(ruleProviderDefs))
	var wg sync.WaitGroup
	for i, rp := range ruleProviderDefs {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			b, err := fetchRule(name+".txt", clients, budget)
			results[i] = fetchResult{b, err}
		}(i, rp.Name)
	}
	wg.Wait()

	fetched := map[string][]byte{}
	var failed []string
	for i, rp := range ruleProviderDefs {
		if results[i].err != nil {
			failed = append(failed, fmt.Sprintf("%s(%v)", rp.Name, results[i].err))
			continue
		}
		fetched[rp.Name] = results[i].b
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("规则集下载失败：%s", strings.Join(failed, "；"))
	}

	meta := &RulesMeta{
		UpdatedAt: time.Now().Format(time.RFC3339),
		Source:    rulesUpstreamRepo,
		Origin:    "online",
		Files:     map[string]int{},
	}
	for _, rp := range ruleProviderDefs {
		b := fetched[rp.Name]
		if err := atomicWrite(filepath.Join(dir, rp.Name+".yaml"), b, 0o644); err != nil {
			return nil, err
		}
		meta.Files[rp.Name] = countRuleLines(b)
	}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(rulesMetaFile(dir), append(mb, '\n'), 0o644); err != nil {
		return nil, err
	}
	return meta, nil
}
