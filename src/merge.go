package main

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type MergeResult struct {
	Config     []byte
	Total      int
	Unique     int
	Duplicated int
	Groups     int
	Rules      int
	Dropped    []string
}

var builtinRefs = map[string]bool{
	"DIRECT": true, "REJECT": true, "PASS": true, "GLOBAL": true,
	"REJECT-DROP": true, "COMPATIBLE": true,
}

func s(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

// proxyKey 用于跨订阅去重：协议 + 服务器 + 端口 + 凭据。
func proxyKey(m map[string]any) string {
	cred := s(m, "uuid")
	if cred == "" {
		cred = s(m, "password")
	}
	if cred == "" {
		cred = s(m, "auth-str")
	}
	return strings.ToLower(strings.Join([]string{s(m, "type"), s(m, "server"), s(m, "port"), cred}, "|"))
}

// Dedupe 按 proxyKey 去重，并保证节点名唯一。
func Dedupe(all []map[string]any) ([]map[string]any, int) {
	seen := map[string]bool{}
	nameUsed := map[string]bool{}
	out := make([]map[string]any, 0, len(all))
	dup := 0
	for _, m := range all {
		if s(m, "type") == "" || s(m, "server") == "" || s(m, "port") == "" {
			continue
		}
		k := proxyKey(m)
		if seen[k] {
			dup++
			continue
		}
		seen[k] = true
		name := s(m, "name")
		if name == "" {
			name = s(m, "server") + ":" + s(m, "port")
		}
		if nameUsed[name] {
			base := name
			for i := 2; ; i++ {
				cand := fmt.Sprintf("%s #%d", base, i)
				if !nameUsed[cand] {
					name = cand
					break
				}
			}
		}
		nameUsed[name] = true
		m["name"] = name
		out = append(out, m)
	}
	return out, dup
}

// SanitizeGroups 净化代理组：移除 provider 引用、过滤失效节点名，空组回退全选。
func SanitizeGroups(raw []any, proxies []map[string]any) []any {
	names := make([]string, 0, len(proxies))
	for _, p := range proxies {
		names = append(names, s(p, "name"))
	}
	groupNames := map[string]bool{}
	type g struct {
		name string
		m    map[string]any
	}
	var list []g
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		n := s(m, "name")
		if n == "" {
			continue
		}
		groupNames[n] = true
		list = append(list, g{n, m})
	}

	valid := func(ref string) bool {
		return builtinRefs[ref] || groupNames[ref]
	}
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}

	out := make([]any, 0, len(list)+1)
	for _, it := range list {
		m := it.m
		delete(m, "use")
		delete(m, "providers")
		var kept []string
		if ps, ok := m["proxies"].([]any); ok {
			for _, p := range ps {
				ref := fmt.Sprint(p)
				if nameSet[ref] || valid(ref) {
					kept = append(kept, ref)
				}
			}
		}
		if len(kept) == 0 {
			if f := s(m, "filter"); f != "" && len(names) > 0 {
				if re, err := regexp.Compile(f); err == nil {
					for _, n := range names {
						if re.MatchString(n) {
							kept = append(kept, n)
						}
					}
				}
			}
		}
		if len(kept) == 0 && len(names) > 0 {
			kept = names
		}
		if len(kept) == 0 {
			kept = []string{"DIRECT"}
		}
		delete(m, "filter")
		delete(m, "exclude-filter")
		m["proxies"] = kept
		out = append(out, m)
	}
	if ExitGroupName(out) == "" {
		// 订阅里没有任何可手动选择的策略组：补一个 PROXY 组，保证规则有合法出口。
		sel := []any{}
		for _, n := range names {
			sel = append(sel, n)
		}
		if len(sel) == 0 {
			sel = []any{"DIRECT"}
		}
		out = append([]any{map[string]any{"name": "PROXY", "type": "select", "proxies": sel}}, out...)
	}
	return out
}

// ExitGroupName 返回规则出口（MATCH）应指向的策略组名：
// 订阅自带名为 PROXY 的组则用它；否则用订阅里第一个可手动选择的组（type=select）；
// 都没有时返回空串，SanitizeGroups 会补一个 PROXY 组兜底。
// 这样界面上就不会出现订阅里并不存在的“幽灵”策略组。
func ExitGroupName(groups []any) string {
	first := ""
	for _, it := range groups {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := s(m, "name")
		if name == "" {
			continue
		}
		if name == "PROXY" {
			return "PROXY"
		}
		if first == "" && strings.EqualFold(s(m, "type"), "select") {
			first = name
		}
	}
	return first
}

var ruleNeedData = []string{"RULE-SET", "GEOSITE", "GEOIP", "SRC-GEOIP", "GEODATA"}

// sanitizeUserRules 过滤订阅自带的规则：丢掉依赖外部数据文件的条目（改用内置离线规则集顶替），
// 并丢掉订阅里的兜底 MATCH（由本应用统一追加，避免国内流量被提前丢进代理）。
func sanitizeUserRules(rules []string) ([]string, int) {
	out := []string{}
	dropCount := 0
	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		rtype := strings.ToUpper(strings.SplitN(r, ",", 2)[0])
		if rtype == "MATCH" || rtype == "FINAL" {
			continue
		}
		bad := false
		for _, b := range ruleNeedData {
			if rtype == b {
				bad = true
				break
			}
		}
		if bad {
			dropCount++
			continue
		}
		out = append(out, r)
	}
	return out, dropCount
}

// SanitizeRules 组装最终规则表，顺序固定为：
//
//	内网直连（保留地址/lancidr/private）→ 订阅自带规则 → 内置国内直连（direct/cncidr）→ MATCH,<订阅主策略组>
//
// 订阅里被丢弃的 GEOIP,CN / GEOSITE,cn 等条目由内置的 direct + cncidr 规则集等价顶替，
// 这样既不依赖联网下载地理数据，也不会出现“国内流量全部走代理”的兜底行为。
func SanitizeRules(rules []string, exit string) ([]string, []string) {
	user, _ := sanitizeUserRules(rules)
	notices := []string{}
	final := []string{}
	seen := map[string]bool{}
	push := func(rs []string) {
		for _, r := range rs {
			if seen[r] {
				continue
			}
			seen[r] = true
			final = append(final, r)
		}
	}
	push(lanDirectRules())
	push(user)
	push(cnDirectRules())
	if exit == "" {
		exit = "PROXY"
	}
	push([]string{"MATCH," + exit})
	return final, notices
}

// BuildConfig 生成最终的 Mihomo 配置。ruleDir 为内置规则集目录（绝对路径，可为空）。
func BuildConfig(proxies []map[string]any, groups []any, rules []string, port int, sock, ruleDir string) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 由 MihomoProxy 自动生成，请勿手工修改（订阅更新会覆盖本文件）\n")
	fmt.Fprintf(&sb, "# 生成时间：%s\n", nowStr())
	fmt.Fprintf(&sb, "mixed-port: %d\n", port)
	sb.WriteString("allow-lan: true\n")
	sb.WriteString("bind-address: '*'\n")
	sb.WriteString("mode: rule\n")
	sb.WriteString("log-level: info\n")
	sb.WriteString("ipv6: false\n")
	sb.WriteString("unified-delay: true\n")
	sb.WriteString("tcp-concurrent: true\n")
	sb.WriteString("find-process-mode: strict\n")
	sb.WriteString("profile:\n  store-selected: true\n  store-fake-ip: false\n")
	fmt.Fprintf(&sb, "external-controller-unix: %q\n", sock)
	sb.WriteString("secret: \"\"\n\n")

	if ruleDir != "" {
		writeSection(&sb, "rule-providers", RuleProvidersSection(ruleDir))
	}
	writeSection(&sb, "proxies", proxies)
	writeSection(&sb, "proxy-groups", groups)
	if len(rules) > 0 {
		writeSection(&sb, "rules", rules)
	}
	return []byte(sb.String())
}

func writeSection(sb *strings.Builder, key string, v any) {
	b, err := yaml.Marshal(map[string]any{key: v})
	if err != nil {
		return
	}
	sb.Write(b)
	sb.WriteString("\n")
}
