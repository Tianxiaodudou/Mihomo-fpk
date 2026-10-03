package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Parsed struct {
	Proxies []map[string]any
	Groups  []any
	Rules   []string
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        16,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}
}

type fetchResult struct {
	Body   []byte
	Info   *SubInfo
	RawURL string
}

// FetchSubscription 下载订阅并解析出流量信息。
func FetchSubscription(rawURL string) (*fetchResult, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("订阅地址无效：%w", err)
	}
	req.Header.Set("User-Agent", "MihomoProxy/1.0 (fnOS; clash-verge)")
	req.Header.Set("Accept", "*/*")
	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求订阅失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("订阅返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("读取订阅内容失败：%w", err)
	}
	return &fetchResult{Body: body, Info: parseSubInfo(resp.Header.Get("subscription-userinfo")), RawURL: rawURL}, nil
}

func parseSubInfo(h string) *SubInfo {
	if h == "" {
		return nil
	}
	info := &SubInfo{}
	found := false
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "upload":
			info.Upload = v
			found = true
		case "download":
			info.Download = v
			found = true
		case "total":
			info.Total = v
			found = true
		case "expire":
			info.Expire = v
			found = true
		}
	}
	if !found {
		return nil
	}
	return info
}

// ParseSubscription 自动识别订阅内容格式：Clash YAML / base64 分享链接 / 明文分享链接 / JSON。
func ParseSubscription(raw []byte) (*Parsed, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil, fmt.Errorf("订阅内容为空")
	}

	// 1) Clash / Mihomo YAML
	if strings.Contains(text, "proxies:") || strings.Contains(text, "proxy-groups:") {
		if p, err := parseClashYAML([]byte(text)); err == nil && len(p.Proxies) > 0 {
			return p, nil
		}
	}

	// 2) JSON 数组（outbound 列表）
	if strings.HasPrefix(text, "[") {
		var arr []map[string]any
		if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
			out := &Parsed{Rules: defaultRules()}
			for _, m := range arr {
				if mm := normalizeJSONProxy(m); mm != nil {
					out.Proxies = append(out.Proxies, mm)
				}
			}
			if len(out.Proxies) > 0 {
				return out, nil
			}
		}
	}

	// 3) 单行 base64（整体编码的分享链接列表）
	if dec, ok := tryBase64(text); ok {
		if p, err := parseShareLinks(dec); err == nil && len(p.Proxies) > 0 {
			return p, nil
		}
	}

	// 4) 明文分享链接
	if p, err := parseShareLinks(text); err == nil && len(p.Proxies) > 0 {
		return p, nil
	}

	// 5) 兜底：当作 YAML 解析
	if p, err := parseClashYAML([]byte(text)); err == nil && len(p.Proxies) > 0 {
		return p, nil
	}
	return nil, fmt.Errorf("无法识别订阅格式（既不是 Clash YAML，也不是分享链接）")
}

func parseClashYAML(raw []byte) (*Parsed, error) {
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	out := &Parsed{}
	if ps, ok := root["proxies"].([]any); ok {
		for _, it := range ps {
			if m, ok := it.(map[string]any); ok {
				out.Proxies = append(out.Proxies, m)
			}
		}
	}
	if gs, ok := root["proxy-groups"].([]any); ok {
		out.Groups = gs
	}
	if rs, ok := root["rules"].([]any); ok {
		for _, r := range rs {
			if s, ok := r.(string); ok {
				out.Rules = append(out.Rules, s)
			}
		}
	}
	for _, k := range []string{"proxy-providers", "rule-providers"} {
		delete(root, k)
	}
	return out, nil
}

func normalizeJSONProxy(m map[string]any) map[string]any {
	if _, ok := m["server"]; !ok {
		if s, _ := m["address"].(string); s != "" {
			m["server"] = s
		}
	}
	if _, ok := m["type"]; !ok {
		return nil
	}
	return m
}

func tryBase64(s string) (string, bool) {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) < 16 || strings.ContainsAny(s, " :") {
		return "", false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) > 0 {
			t := string(b)
			if strings.Contains(t, "://") || strings.Contains(t, "@") {
				return t, true
			}
		}
	}
	return "", false
}

var linkSplitRe = regexp.MustCompile(`\s+`)

func parseShareLinks(text string) (*Parsed, error) {
	out := &Parsed{Rules: defaultRules()}
	for _, line := range linkSplitRe.Split(strings.TrimSpace(text), -1) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		var err error
		switch {
		case strings.HasPrefix(line, "ss://"):
			m, err = parseSS(line)
		case strings.HasPrefix(line, "ssr://"):
			m, err = parseSSR(line)
		case strings.HasPrefix(line, "vmess://"):
			m, err = parseVMess(line)
		case strings.HasPrefix(line, "vless://"):
			m, err = parseVLESS(line)
		case strings.HasPrefix(line, "trojan://"):
			m, err = parseTrojan(line)
		case strings.HasPrefix(line, "hysteria2://"), strings.HasPrefix(line, "hy2://"):
			m, err = parseHysteria2(line)
		case strings.HasPrefix(line, "hysteria://"):
			m, err = parseHysteria(line)
		case strings.HasPrefix(line, "tuic://"):
			m, err = parseTUIC(line)
		default:
			continue
		}
		if err == nil && m != nil {
			out.Proxies = append(out.Proxies, m)
		}
	}
	if len(out.Proxies) == 0 {
		return nil, fmt.Errorf("未解析出任何节点")
	}
	return out, nil
}

func defaultRules() []string {
	return []string{
		"GEOIP,LAN,DIRECT,no-resolve",
		"GEOIP,CN,DIRECT",
		"MATCH,PROXY",
	}
}

func u2q(u *url.URL) url.Values { return u.Query() }

func fragName(u *url.URL, def string) string {
	if u.Fragment != "" {
		if n, err := url.QueryUnescape(u.Fragment); err == nil && n != "" {
			return n
		}
		return u.Fragment
	}
	return def
}

func setIf(m map[string]any, k string, u *url.URL, qk string) {
	if v := u2q(u).Get(qk); v != "" {
		m[k] = v
	}
}
