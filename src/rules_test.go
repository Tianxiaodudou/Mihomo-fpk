package main

import (
	"strings"
	"testing"
)

// TestSanitizeRulesOrder 锁定规则顺序，防止以后重构把「内网直连 / 强制代理 / 国内直连」的优先级改错。
// 期望顺序：内网保留地址 → lancidr/private → gfw（被墙域名强制代理）→ direct/cncidr（国内直连）
// → 订阅自带规则 → MATCH,<出口>。
// 注意 direct 表里包含苹果/微软等国内可直连的域名，它们不在 gfw 表内，因此仍然直连。
func TestSanitizeRulesOrder(t *testing.T) {
	sub := []string{
		"GEOIP,CN,DIRECT",                  // 需要地理数据文件，应被丢弃
		"GEOSITE,cn,DIRECT",                // 同上
		"DOMAIN-SUFFIX,example.com,DIRECT", // 订阅自定义规则，应保留且排在最后
		"RULE-SET,some-provider,PROXY",     // 需要外部提供者，应被丢弃
		"MATCH,PROXY",                      // 不得重复出现
	}
	got, _ := SanitizeRules(sub, "PROXY")

	idx := func(prefix string) int {
		for i, r := range got {
			if strings.HasPrefix(r, prefix) {
				return i
			}
		}
		return -1
	}
	order := []struct {
		name   string
		prefix string
	}{
		{"内网", "IP-CIDR,192.168.0.0/16"},
		{"lancidr", "RULE-SET,lancidr,"},
		{"private", "RULE-SET,private,"},
		{"gfw强制代理", "RULE-SET,gfw,"},
		{"国内直连-域名", "RULE-SET,direct,"},
		{"国内直连-IP", "RULE-SET,cncidr,"},
		{"订阅规则", "DOMAIN-SUFFIX,example.com,"},
	}
	prevName, prevIdx := "", -1
	for _, o := range order {
		i := idx(o.prefix)
		if i < 0 {
			t.Fatalf("缺少规则 %s（prefix=%s）\n最终规则表：\n%s", o.name, o.prefix, strings.Join(got, "\n"))
		}
		if i < prevIdx {
			t.Fatalf("顺序错误：%s(#%d) 应排在 %s(#%d) 之后\n最终规则表：\n%s",
				o.name, i, prevName, prevIdx, strings.Join(got, "\n"))
		}
		prevName, prevIdx = o.name, i
	}

	if last := got[len(got)-1]; last != "MATCH,PROXY" {
		t.Fatalf("最后一条应为 MATCH,PROXY，实际为 %q", last)
	}
	for _, r := range got {
		if strings.HasPrefix(r, "GEOIP,") || strings.HasPrefix(r, "GEOSITE,") || strings.HasPrefix(r, "RULE-SET,some-provider,") {
			t.Fatalf("依赖外部数据的规则未被丢弃: %s", r)
		}
		if strings.HasPrefix(r, "MATCH,") && r != "MATCH,PROXY" {
			t.Fatalf("出现多余的 MATCH: %s", r)
		}
	}
	if r := idx("RULE-SET,gfw,"); r >= 0 && got[r] != "RULE-SET,gfw,PROXY" {
		t.Fatalf("gfw 出口未指向订阅主策略组: %q", got[r])
	}
	t.Logf("最终规则表：\n  %s", strings.Join(got, "\n  "))
}

// TestRuleProviderDefs 确保内置规则集与 gfw 都已注册（配置里会生成对应的 rule-providers 段）。
func TestRuleProviderDefs(t *testing.T) {
	want := map[string]string{"private": "domain", "direct": "domain", "gfw": "domain", "lancidr": "ipcidr", "cncidr": "ipcidr"}
	have := map[string]string{}
	for _, d := range ruleProviderDefs {
		have[d.Name] = d.Behavior
		if _, err := ruleFS.ReadFile(d.File); err != nil {
			t.Fatalf("内嵌规则文件缺失: %s (%v)", d.File, err)
		}
	}
	for name, behavior := range want {
		if have[name] != behavior {
			t.Fatalf("规则集 %s 缺失或 behavior 错误（期望 %s，实际 %q）", name, behavior, have[name])
		}
	}
}
