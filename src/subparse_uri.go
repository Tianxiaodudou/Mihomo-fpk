package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func b64any(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// splitHostPort 解析 host:port，兼容 IPv6 字面量 [::1]:443。
func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("缺少端口：%s", s)
	}
	host := s[:i]
	port, err := strconv.Atoi(strings.Trim(s[i+1:], "/"))
	if err != nil {
		return "", 0, fmt.Errorf("端口非法：%s", s)
	}
	host = strings.Trim(host, "[]")
	return host, port, nil
}

func parseSS(line string) (map[string]any, error) {
	raw := strings.TrimPrefix(line, "ss://")
	name := ""
	if i := strings.Index(raw, "#"); i >= 0 {
		if n, err := url.QueryUnescape(raw[i+1:]); err == nil {
			name = n
		} else {
			name = raw[i+1:]
		}
		raw = raw[:i]
	}
	plugin := ""
	if i := strings.Index(raw, "?"); i >= 0 {
		if q, err := url.ParseQuery(raw[i+1:]); err == nil {
			plugin = q.Get("plugin")
		}
		raw = raw[:i]
	}
	var userinfo, hostport string
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		userinfo = raw[:i]
		hostport = raw[i+1:]
	} else {
		dec, ok := b64any(raw)
		if !ok {
			return nil, fmt.Errorf("ss 链接无法解码")
		}
		i := strings.LastIndex(dec, "@")
		if i < 0 {
			return nil, fmt.Errorf("ss 链接缺少 @")
		}
		userinfo = dec[:i]
		hostport = dec[i+1:]
	}
	if dec, ok := b64any(userinfo); ok && strings.Contains(dec, ":") {
		userinfo = dec
	}
	method, pass, ok := strings.Cut(userinfo, ":")
	if !ok {
		return nil, fmt.Errorf("ss 链接缺少密码")
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	m := map[string]any{
		"name": name, "type": "ss", "server": host, "port": port,
		"cipher": method, "password": pass, "udp": true,
	}
	if plugin != "" {
		parts := strings.Split(plugin, ";")
		m["plugin"] = parts[0]
		for _, p := range parts[1:] {
			if k, v, ok := strings.Cut(p, "="); ok {
				m["plugin-opts"] = mergeKV(m["plugin-opts"], k, v)
			}
		}
	}
	return m, nil
}

func mergeKV(existing any, k, v string) map[string]any {
	m, _ := existing.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	if b, err := strconv.ParseBool(v); err == nil {
		m[k] = b
	} else {
		m[k] = v
	}
	return m
}

func parseSSR(line string) (map[string]any, error) {
	raw := strings.TrimPrefix(line, "ssr://")
	dec, ok := b64any(raw)
	if !ok {
		return nil, fmt.Errorf("ssr 链接无法解码")
	}
	main, query, _ := strings.Cut(dec, "/?")
	parts := strings.Split(main, ":")
	if len(parts) < 6 {
		return nil, fmt.Errorf("ssr 链接字段不足")
	}
	pass, _ := b64any(parts[5])
	host := strings.Join(parts[:len(parts)-5], ":")
	port, err := strconv.Atoi(parts[len(parts)-5])
	if err != nil {
		return nil, fmt.Errorf("ssr 端口非法")
	}
	m := map[string]any{
		"name": host + ":" + strconv.Itoa(port), "type": "ssr", "server": host, "port": port,
		"cipher": parts[len(parts)-3], "protocol": parts[len(parts)-4],
		"obfs": parts[len(parts)-2], "password": pass, "udp": true,
	}
	if query != "" {
		q, _ := url.ParseQuery(query)
		if v, ok := b64any(q.Get("remarks")); ok && v != "" {
			m["name"] = v
		}
		if v, ok := b64any(q.Get("obfsparam")); ok && v != "" {
			m["obfs-param"] = v
		}
		if v, ok := b64any(q.Get("protoparam")); ok && v != "" {
			m["protocol-param"] = v
		}
	}
	return m, nil
}

func parseVMess(line string) (map[string]any, error) {
	raw := strings.TrimPrefix(line, "vmess://")
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	dec, ok := b64any(raw)
	if !ok {
		return nil, fmt.Errorf("vmess 链接无法解码")
	}
	var v struct {
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port any    `json:"port"`
		ID   string `json:"id"`
		Aid  any    `json:"aid"`
		Scy  string `json:"scy"`
		Net  string `json:"net"`
		Type string `json:"type"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
		ALPN string `json:"alpn"`
		FP   string `json:"fp"`
	}
	if err := json.Unmarshal([]byte(dec), &v); err != nil {
		return nil, fmt.Errorf("vmess JSON 解析失败")
	}
	port := toInt(v.Port)
	if v.Add == "" || port == 0 || v.ID == "" {
		return nil, fmt.Errorf("vmess 关键字段缺失")
	}
	name := v.PS
	if name == "" {
		name = v.Add + ":" + strconv.Itoa(port)
	}
	m := map[string]any{
		"name": name, "type": "vmess", "server": v.Add, "port": port,
		"uuid": v.ID, "alterId": toInt(v.Aid), "cipher": orDefault(v.Scy, "auto"), "udp": true,
	}
	applyTransport(m, v.Net, v.Host, v.Path, v.Type, "")
	if v.TLS == "tls" || v.SNI != "" {
		m["tls"] = true
	}
	if v.SNI != "" {
		m["servername"] = v.SNI
	}
	if v.ALPN != "" {
		m["alpn"] = strings.Split(v.ALPN, ",")
	}
	if v.FP != "" {
		m["client-fingerprint"] = v.FP
	}
	return m, nil
}

// applyTransport 把分享链接的传输层参数转换为 Mihomo 字段。
func applyTransport(m map[string]any, netw, host, path, htype, serviceName string) {
	switch netw {
	case "ws":
		opts := map[string]any{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		m["network"] = "ws"
		if len(opts) > 0 {
			m["ws-opts"] = opts
		}
	case "grpc":
		m["network"] = "grpc"
		opts := map[string]any{}
		if serviceName != "" {
			opts["grpc-service-name"] = serviceName
		} else if path != "" {
			opts["grpc-service-name"] = path
		}
		if len(opts) > 0 {
			m["grpc-opts"] = opts
		}
	case "h2", "http":
		m["network"] = "h2"
		opts := map[string]any{}
		if path != "" {
			opts["path"] = path
		}
		if host != "" {
			opts["host"] = []string{host}
		}
		if len(opts) > 0 {
			m["h2-opts"] = opts
		}
	case "tcp":
		if htype == "http" {
			m["network"] = "http"
			opts := map[string]any{}
			if path != "" {
				opts["path"] = []string{path}
			}
			if host != "" {
				opts["headers"] = map[string]any{"Host": []string{host}}
			}
			if len(opts) > 0 {
				m["http-opts"] = opts
			}
		}
	}
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(t)
		return n
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	}
	return 0
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
