package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) kernelOffline(w http.ResponseWriter) bool {
	if !a.Mihomo.Running() {
		writeErr(w, 409, "内核未运行")
		return true
	}
	return false
}

// hProxies 内核运行时直接透传内核数据；未运行时从本地 config.yaml 合成，保证列表可见。
func (a *App) hProxies(w http.ResponseWriter, r *http.Request) {
	if a.Mihomo.Running() {
		if b, code, err := a.Mihomo.API(http.MethodGet, "/proxies", nil); err == nil && code == 200 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write(b)
			return
		}
	}
	nodes, groups := a.ConfigNodes()
	names := []string{}
	proxies := map[string]any{}
	for _, n := range nodes {
		proxies[n.Name] = map[string]any{"name": n.Name, "type": n.Type, "all": []string{}, "now": "", "offline": true}
		names = append(names, n.Name)
	}
	if len(groups) == 0 {
		groups = []string{"PROXY"}
	}
	for _, g := range groups {
		proxies[g] = map[string]any{"name": g, "type": "Selector", "all": names, "now": "", "offline": true}
	}
	writeJSON(w, 200, map[string]any{"proxies": proxies, "running": false})
}

func (a *App) hSelect(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	var body struct {
		Group string `json:"group"`
		Name  string `json:"name"`
	}
	b, _ := readBody(r, 1<<16)
	if err := json.Unmarshal(b, &body); err != nil || body.Group == "" || body.Name == "" {
		writeErr(w, 400, "缺少 group / name")
		return
	}
	raw, code, err := a.Mihomo.API(http.MethodPut, "/proxies/"+url.PathEscape(body.Group), map[string]string{"name": body.Name})
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if code >= 300 {
		writeErr(w, 502, fmt.Sprintf("内核返回 %d：%s", code, strings.TrimSpace(string(raw))))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) hNodeDelay(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	q := url.Values{}
	q.Set("timeout", orDefault(r.URL.Query().Get("timeout"), "5000"))
	if u := r.URL.Query().Get("url"); u != "" {
		q.Set("url", u)
	}
	b, code, err := a.Mihomo.API(http.MethodGet, "/proxies/"+url.PathEscape(r.PathValue("name"))+"/delay?"+q.Encode(), nil)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (a *App) hGroupDelay(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	q := url.Values{}
	q.Set("timeout", orDefault(r.URL.Query().Get("timeout"), "5000"))
	if u := r.URL.Query().Get("url"); u != "" {
		q.Set("url", u)
	}
	b, code, err := a.Mihomo.API(http.MethodGet, "/group/"+url.PathEscape(r.PathValue("name"))+"/delay?"+q.Encode(), nil)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (a *App) hConnections(w http.ResponseWriter, r *http.Request) {
	if a.Mihomo.Running() {
		if b, code, err := a.Mihomo.API(http.MethodGet, "/connections", nil); err == nil && code == 200 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write(b)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"connections": []any{}, "uploadTotal": 0, "downloadTotal": 0, "running": false})
}

func (a *App) hConnectionsDelete(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	_, code, err := a.Mihomo.API(http.MethodDelete, "/connections", nil)
	if err != nil || code >= 300 {
		writeErr(w, 502, "关闭全部连接失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) hConnectionDelete(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	_, code, err := a.Mihomo.API(http.MethodDelete, "/connections/"+url.PathEscape(r.PathValue("id")), nil)
	if err != nil || code >= 300 {
		writeErr(w, 502, "关闭连接失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func logLevel(r *http.Request) string {
	lv := r.URL.Query().Get("level")
	switch lv {
	case "debug", "info", "warning", "error", "silent":
		return lv
	}
	return "info"
}

func sseLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}

// hLogsSSE 把内核日志流以 SSE 形式转发给前端。
func (a *App) hLogsSSE(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "当前服务不支持流式响应")
		return
	}
	ws, err := DialWS("/logs?level=" + logLevel(r))
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer ws.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprintf(w, "event: ready\ndata: {\"level\":\"%s\"}\n\n", logLevel(r))
	fl.Flush()

	done := make(chan struct{})
	go func() {
		<-r.Context().Done()
		ws.Close()
		close(done)
	}()
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if ws.WritePing() != nil {
					return
				}
			}
		}
	}()
	for {
		op, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		switch op {
		case 1:
			fmt.Fprintf(w, "data: %s\n\n", sseLine(string(data)))
			fl.Flush()
		case 8:
			return
		case 9:
			_ = ws.writeFrame(10, data)
		}
	}
}

// hLogsWS 以 WebSocket 转发内核日志（供不支持 SSE 的场景使用）。
func (a *App) hLogsWS(w http.ResponseWriter, r *http.Request) {
	if a.kernelOffline(w) {
		return
	}
	client, err := UpgradeServer(w, r)
	if err != nil {
		return
	}
	defer client.Close()
	up, err := DialWS("/logs?level=" + logLevel(r))
	if err != nil {
		_ = client.WriteText([]byte("{\"error\":" + strconv.Quote(err.Error()) + "}"))
		return
	}
	defer up.Close()
	go func() {
		for {
			op, data, err := up.ReadMessage()
			if err != nil {
				_ = client.WriteClose()
				client.Close()
				return
			}
			if op == 1 {
				if client.WriteText(data) != nil {
					return
				}
			} else if op == 8 {
				_ = client.WriteClose()
				return
			}
		}
	}()
	for {
		op, data, err := client.ReadMessage()
		if err != nil {
			return
		}
		switch op {
		case 8:
			return
		case 9:
			_ = up.writeFrame(10, data)
		}
	}
}
