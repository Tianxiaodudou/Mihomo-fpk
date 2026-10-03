package main

import (
	"fmt"
	"net/url"
	"strings"
)

var tlsLike = map[string]bool{"tls": true, "reality": true, "xtls": true}

func parseVLESS(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("vless 链接无效")
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	uuid := u.User.Username()
	if uuid == "" {
		return nil, fmt.Errorf("vless 缺少 uuid")
	}
	q := u.Query()
	m := map[string]any{"name": fragName(u, host+":"+fmt.Sprint(port)), "type": "vless",
		"server": host, "port": port, "uuid": uuid, "udp": true}
	if sec := q.Get("security"); sec != "" && sec != "none" {
		m["tls"] = tlsLike[sec]
		if sec == "reality" {
			m["tls"] = true
		}
	}
	if v := q.Get("sni"); v != "" {
		m["servername"] = v
	} else if v := q.Get("peer"); v != "" {
		m["servername"] = v
	}
	if v := q.Get("fp"); v != "" {
		m["client-fingerprint"] = v
	}
	if v := q.Get("flow"); v != "" {
		m["flow"] = v
	}
	if v := q.Get("pbk"); v != "" {
		ro := map[string]any{"public-key": v}
		if sid := q.Get("sid"); sid != "" {
			ro["short-id"] = sid
		}
		m["reality-opts"] = ro
	}
	if v := q.Get("alpn"); v != "" {
		m["alpn"] = strings.Split(v, ",")
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
		m["skip-cert-verify"] = true
	}
	applyTransport(m, q.Get("type"), q.Get("host"), pathOf(q), "http", q.Get("serviceName"))
	return m, nil
}

func pathOf(q url.Values) string {
	if v := q.Get("path"); v != "" {
		return v
	}
	return ""
}

func parseTrojan(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("trojan 链接无效")
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	pw := u.User.Username()
	if p, ok := u.User.Password(); ok && p != "" {
		pw = pw + ":" + p
	}
	if pw == "" {
		return nil, fmt.Errorf("trojan 缺少密码")
	}
	q := u.Query()
	m := map[string]any{"name": fragName(u, host+":"+fmt.Sprint(port)), "type": "trojan",
		"server": host, "port": port, "password": pw, "udp": true}
	if v := q.Get("sni"); v != "" {
		m["sni"] = v
	} else if v := q.Get("peer"); v != "" {
		m["sni"] = v
	}
	if v := q.Get("alpn"); v != "" {
		m["alpn"] = strings.Split(v, ",")
	}
	if v := q.Get("fp"); v != "" {
		m["client-fingerprint"] = v
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
		m["skip-cert-verify"] = true
	}
	applyTransport(m, q.Get("type"), q.Get("host"), pathOf(q), "http", q.Get("serviceName"))
	return m, nil
}

func parseHysteria2(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("hysteria2 链接无效")
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	auth := u.User.Username()
	if p, ok := u.User.Password(); ok && p != "" {
		auth = auth + ":" + p
	}
	q := u.Query()
	m := map[string]any{"name": fragName(u, host+":"+fmt.Sprint(port)), "type": "hysteria2",
		"server": host, "port": port, "password": auth, "udp": true}
	if v := q.Get("sni"); v != "" {
		m["sni"] = v
	} else if v := q.Get("peer"); v != "" {
		m["sni"] = v
	}
	if q.Get("insecure") == "1" || q.Get("allowInsecure") == "1" {
		m["skip-cert-verify"] = true
	}
	if v := q.Get("obfs"); v != "" {
		m["obfs"] = v
		if p := q.Get("obfs-password"); p != "" {
			m["obfs-password"] = p
		}
	}
	if v := q.Get("pinSHA256"); v != "" {
		m["fingerprint"] = v
	}
	if v := q.Get("alpn"); v != "" {
		m["alpn"] = strings.Split(v, ",")
	}
	return m, nil
}

func parseHysteria(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("hysteria 链接无效")
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	m := map[string]any{"name": fragName(u, host+":"+fmt.Sprint(port)), "type": "hysteria",
		"server": host, "port": port, "protocol": orDefault(q.Get("protocol"), "udp"), "udp": true}
	if v := q.Get("auth"); v != "" {
		m["auth-str"] = v
	} else if u.User.Username() != "" {
		m["auth-str"] = u.User.Username()
	}
	if v := q.Get("peer"); v != "" {
		m["sni"] = v
	} else if v := q.Get("sni"); v != "" {
		m["sni"] = v
	}
	if q.Get("insecure") == "1" || q.Get("allowInsecure") == "1" {
		m["skip-cert-verify"] = true
	}
	if v := q.Get("alpn"); v != "" {
		m["alpn"] = strings.Split(v, ",")
	}
	if v := q.Get("obfs"); v != "" {
		m["obfs"] = v
	}
	if v := q.Get("upmbps"); v != "" {
		m["up"] = v
	}
	if v := q.Get("downmbps"); v != "" {
		m["down"] = v
	}
	return m, nil
}

func parseTUIC(line string) (map[string]any, error) {
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("tuic 链接无效")
	}
	host, port, err := splitHostPort(u.Host)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	m := map[string]any{"name": fragName(u, host+":"+fmt.Sprint(port)), "type": "tuic",
		"server": host, "port": port, "uuid": u.User.Username()}
	if p, ok := u.User.Password(); ok {
		m["password"] = p
	}
	if v := q.Get("sni"); v != "" {
		m["sni"] = v
	}
	if v := q.Get("alpn"); v != "" {
		m["alpn"] = strings.Split(v, ",")
	}
	if v := q.Get("congestion_control"); v != "" {
		m["congestion-controller"] = v
	}
	if v := q.Get("udp_relay_mode"); v != "" {
		m["udp-relay-mode"] = v
	}
	if q.Get("allow_insecure") == "1" || q.Get("insecure") == "1" {
		m["skip-cert-verify"] = true
	}
	return m, nil
}
