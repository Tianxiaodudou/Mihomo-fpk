package main

import (
	"embed"
	"os"
	"path/filepath"
)

// 内置离线分流规则集（Clash rule-provider 格式，yaml）。
// 来源：Loyalsoldier/clash-rules release 分支（direct/private/lancidr/cncidr/gfw），
// 打包进二进制，运行期释放到 <var>/rules/，让内核在完全离线时也能分流。
// 构建时由 tools/build.py 的 rules 步骤拉取上游最新版本，失败则沿用仓内快照。
//
//go:embed rules/*.txt
var ruleFS embed.FS

type ruleProviderDef struct {
	Name     string // 配置里 rule-providers 的 key，同时是 RULE-SET 引用的名字
	File     string // 内嵌文件名
	Behavior string // domain / ipcidr
}

var ruleProviderDefs = []ruleProviderDef{
	{Name: "private", File: "rules/private.txt", Behavior: "domain"},
	{Name: "direct", File: "rules/direct.txt", Behavior: "domain"},
	{Name: "gfw", File: "rules/gfw.txt", Behavior: "domain"},
	{Name: "lancidr", File: "rules/lancidr.txt", Behavior: "ipcidr"},
	{Name: "cncidr", File: "rules/cncidr.txt", Behavior: "ipcidr"},
}

// EnsureRuleFiles 把内置规则集释放到 <varDir>/rules/，内容有变化时覆盖。
// 返回规则目录的绝对路径。
func EnsureRuleFiles(varDir string) (string, error) {
	dir := filepath.Join(varDir, "rules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}
	// 用户在设置页手动更新过规则：保留其版本，不要用内置快照覆盖
	// （否则「只更新规则、不升级应用」会被每次重建配置打回原形）。
	if onlineRulesActive(dir) {
		return dir, nil
	}
	for _, rp := range ruleProviderDefs {
		want, err := ruleFS.ReadFile(rp.File)
		if err != nil {
			return dir, err
		}
		dst := filepath.Join(dir, rp.Name+".yaml")
		if cur, err := os.ReadFile(dst); err == nil && len(cur) == len(want) && string(cur) == string(want) {
			continue
		}
		if err := atomicWrite(dst, want, 0o644); err != nil {
			return dir, err
		}
	}
	return dir, nil
}

// RuleProvidersSection 生成写进配置的 rule-providers 段。
// 路径用绝对路径，避免内核工作目录变化时找不到文件。
func RuleProvidersSection(ruleDir string) map[string]any {
	out := map[string]any{}
	for _, rp := range ruleProviderDefs {
		out[rp.Name] = map[string]any{
			"type":     "file",
			"behavior": rp.Behavior,
			"format":   "yaml",
			"path":     filepath.Join(ruleDir, rp.Name+".yaml"),
		}
	}
	return out
}

// lanDirectRules 内网 / 保留地址直连，永远放最前，避免访问 NAS、路由器、内网服务被代理。
func lanDirectRules() []string {
	return []string{
		"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
		"RULE-SET,lancidr,DIRECT,no-resolve",
		"RULE-SET,private,DIRECT",
	}
}

// cnDirectRules 国内域名 / 国内 IP 直连（订阅里被丢弃的 GEOIP,CN / GEOSITE,cn 由这里顶上）。
func cnDirectRules() []string {
	return []string{
		"RULE-SET,direct,DIRECT",
		"RULE-SET,cncidr,DIRECT,no-resolve",
	}
}

// gfwProxyRules 被墙域名强制走代理，排在 cnDirectRules 之前。
// 理由：少数被墙站点（或其 CDN 落在国内 IP）的域名会同时出现在 direct 表里，
// 先命中本组可避免被误判为直连而连接重置；苹果/微软等国内可直连的域名不在本表内，
// 仍由 direct 规则保持直连。
func gfwProxyRules(exit string) []string {
	return []string{"RULE-SET,gfw," + exit}
}
