package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// appVersion 由构建时注入（tools/build.py 的 -ldflags -X main.appVersion=<manifest version>），
// 默认值仅用于本地 go run/build 调试。
var appVersion = "1.0.17"

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readBody(r *http.Request, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, max))
}

// Register 注册全部 API 路由。
func (a *App) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", a.hStatus)
	mux.HandleFunc("GET /api/version", a.hVersion)
	mux.HandleFunc("GET /api/power", a.hPowerGet)
	mux.HandleFunc("PUT /api/power", a.hPowerSet)
	mux.HandleFunc("POST /api/power", a.hPowerSet)

	mux.HandleFunc("GET /api/subs", a.hSubsList)
	mux.HandleFunc("POST /api/subs", a.hSubAdd)
	mux.HandleFunc("PUT /api/subs/{id}", a.hSubUpdate)
	mux.HandleFunc("DELETE /api/subs/{id}", a.hSubDelete)
	mux.HandleFunc("POST /api/subs/{id}/update", a.hSubUpdateOne)
	mux.HandleFunc("POST /api/subs/update", a.hSubUpdateAll)
	mux.HandleFunc("POST /api/subs/import", a.hSubImport)
	mux.HandleFunc("POST /api/subs/reorder", a.hSubsReorder)
	mux.HandleFunc("POST /api/subs/{id}/activate", a.hSubActivate)
	mux.HandleFunc("POST /api/config/rebuild", a.hRebuild)
	mux.HandleFunc("GET /api/rules", a.hRulesInfo)
	mux.HandleFunc("POST /api/rules/update", a.hRulesUpdate)
	mux.HandleFunc("GET /api/config", a.hConfigText)

	mux.HandleFunc("GET /api/proxies", a.hProxies)
	mux.HandleFunc("PUT /api/proxies/select", a.hSelect)
	mux.HandleFunc("GET /api/proxies/{name}/delay", a.hNodeDelay)
	mux.HandleFunc("GET /api/groups/{name}/delay", a.hGroupDelay)

	mux.HandleFunc("GET /api/connections", a.hConnections)
	mux.HandleFunc("DELETE /api/connections", a.hConnectionsDelete)
	mux.HandleFunc("DELETE /api/connections/{id}", a.hConnectionDelete)

	mux.HandleFunc("GET /api/logs", a.hLogsSSE)
	mux.HandleFunc("GET /api/logs/ws", a.hLogsWS)

	mux.HandleFunc("GET /api/settings", a.hSettingsGet)
	mux.HandleFunc("PUT /api/settings", a.hSettingsSet)
}

var (
	mihomoVerMu   sync.Mutex
	mihomoVer     string
	mihomoVerTime time.Time
)

func (a *App) mihomoVersion() string {
	mihomoVerMu.Lock()
	defer mihomoVerMu.Unlock()
	// 仅缓存成功结果：内核刚启动时 /version 可能暂时不可用，若把空值也缓存 60s，
	// 概览/设置页会在这段时间内误显示「未运行」。
	if mihomoVer != "" && time.Since(mihomoVerTime) < 60*time.Second {
		return mihomoVer
	}
	if !a.Mihomo.Running() {
		mihomoVer = ""
		mihomoVerTime = time.Now()
		return ""
	}
	b, code, err := a.Mihomo.API(http.MethodGet, "/version", nil)
	if err != nil || code != 200 {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &v) == nil && v.Version != "" {
		mihomoVer = v.Version
		mihomoVerTime = time.Now()
	}
	return mihomoVer
}

func (a *App) hVersion(w http.ResponseWriter, r *http.Request) {
	rulesVer, rulesOrigin := RulesVersion(a.Paths.Var)
	writeJSON(w, 200, map[string]any{
		"app":           appVersion,
		"rules":         rulesVer,
		"rules_origin":  rulesOrigin,
		"mihomo":        a.mihomoVersion(),
		"go":            runtime.Version(),
		"arch":          a.Paths.Arch,
		"gatewayPrefix": a.Paths.GatewayPrefix,
	})
}

func (a *App) hStatus(w http.ResponseWriter, r *http.Request) {
	s := loadSettings()
	nodes, groups := a.ConfigNodes()
	mv := ""
	if a.Mihomo.Running() {
		mv = a.mihomoVersion()
	}
	activeID, activeName := "", ""
	if x := a.Subs.Active(); x != nil {
		activeID, activeName = x.ID, x.Name
	}
	swInfo, swAt, swErr := a.AutoSwitchInfo()
	writeJSON(w, 200, map[string]any{
		"running":       a.Mihomo.Running(),
		"port":          s.ProxyPort,
		"listen":        "0.0.0.0:" + strconv.Itoa(s.ProxyPort),
		"arch":          a.Paths.Arch,
		"app_version":   appVersion,
		"mihomo_version": mv,
		"sub_count":     len(a.Subs.List()),
		"node_count":    len(nodes),
		"group_count":   len(groups),
		"updating":      a.updating,
		"last_update":   s.LastUpdate,
		"auto_update":   s.AutoUpdateEnabled,
		"auto_hours":    s.AutoUpdateHours,
		"last_error":    a.Mihomo.LastError(),
		"notices":       a.notices,
		"active_id":     activeID,
		"active_sub":    activeName,
		"auto_switch":   s.AutoSwitch,
		"switch_info":   swInfo,
		"switch_at":     swAt,
		"switch_err":    swErr,
		"config_file":   a.Paths.ConfigFile(),
		"socket":        a.Paths.MihomoSock(),
		"now":           nowStr(),
	})
}

func (a *App) hPowerGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"running": a.PowerState()})
}

func (a *App) hPowerSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enable *bool `json:"enable"`
	}
	b, _ := readBody(r, 1<<16)
	_ = json.Unmarshal(b, &body)
	on := true
	if body.Enable != nil {
		on = *body.Enable
	} else if q := r.URL.Query().Get("enable"); q != "" {
		on = q == "1" || q == "true"
	}
	if err := a.SetPower(on); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"running": a.PowerState(), "ok": true})
}

// ---------- 订阅 ----------

func (a *App) subsPayload() map[string]any {
	active := ""
	if x := a.Subs.Active(); x != nil {
		active = x.ID
	}
	info, at, isErr := a.AutoSwitchInfo()
	return map[string]any{
		"subs":        a.Subs.List(),
		"notices":     a.notices,
		"updating":    a.updating,
		"active_id":   active,
		"auto_switch": loadSettings().AutoSwitch,
		"switch_info": info,
		"switch_at":   at,
		"switch_err":  isErr,
	}
}

func (a *App) hSubsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.subsPayload())
}

func (a *App) hSubAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	b, _ := readBody(r, 1<<20)
	if err := json.Unmarshal(b, &body); err != nil || body.URL == "" {
		writeErr(w, 400, "缺少订阅地址 url")
		return
	}
	sub, err := a.Subs.Add(body.Name, body.URL)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.activateIfNone(sub.ID) // 列表中还没有激活订阅时，新添加的订阅自动激活
	_, errs, _ := a.Refresh([]string{sub.ID}, true)
	writeJSON(w, 200, map[string]any{"sub": a.Subs.Get(sub.ID), "errors": errs, "subs": a.Subs.List()})
}

func (a *App) hSubUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Subs.Get(id) == nil {
		writeErr(w, 404, "订阅不存在")
		return
	}
	var body struct {
		Name    *string `json:"name"`
		URL     *string `json:"url"`
		Enabled *bool   `json:"enabled"`
	}
	b, _ := readBody(r, 1<<20)
	_ = json.Unmarshal(b, &body)
	// 名称 / 地址为普通字段修改
	if body.Name != nil || body.URL != nil {
		_, err := a.Subs.Update(id, func(s *Subscription) {
			if body.Name != nil && *body.Name != "" {
				s.Name = *body.Name
			}
			if body.URL != nil {
				s.URL = *body.URL
			}
		})
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// enabled = 激活状态：同一时间只允许一个订阅处于激活状态
	if body.Enabled != nil {
		if _, err := a.Subs.Activate(id, *body.Enabled); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	var errs []string
	if body.URL != nil && *body.URL != "" {
		_, errs, _ = a.Refresh([]string{id}, true)
	} else {
		a.Refresh([]string{}, true)
	}
	active := ""
	if x := a.Subs.Active(); x != nil {
		active = x.ID
	}
	writeJSON(w, 200, map[string]any{"sub": a.Subs.Get(id), "errors": errs, "subs": a.Subs.List(), "active_id": active})
}

func (a *App) hSubDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Subs.Remove(id); err != nil {
		writeErr(w, 404, "订阅不存在")
		return
	}
	_ = os.Remove(a.cacheFile(id))
	_, _, _ = a.Refresh([]string{}, true)
	writeJSON(w, 200, map[string]any{"ok": true, "subs": a.Subs.List()})
}

func (a *App) hSubUpdateOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Subs.Get(id) == nil {
		writeErr(w, 404, "订阅不存在")
		return
	}
	_, errs, err := a.Refresh([]string{id}, true)
	code := 200
	if err != nil {
		code = 500
	}
	writeJSON(w, code, map[string]any{"errors": errs, "sub": a.Subs.Get(id), "notices": a.notices})
}

// hSubActivate 激活 / 取消激活订阅。同一时间只允许一个订阅处于激活状态：
// 激活某个订阅时，其余订阅会自动取消激活（节点列表只显示激活订阅的节点）。
func (a *App) hSubActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Subs.Get(id) == nil {
		writeErr(w, 404, "订阅不存在")
		return
	}
	var body struct {
		Active *bool `json:"active"`
	}
	b, _ := readBody(r, 1<<20)
	_ = json.Unmarshal(b, &body)
	on := true
	if body.Active != nil {
		on = *body.Active
	}
	if _, err := a.Subs.Activate(id, on); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	errs := []string{}
	if _, err := a.RebuildAndReload(); err != nil {
		errs = append(errs, err.Error())
	}
	active := ""
	if x := a.Subs.Active(); x != nil {
		active = x.ID
	}
	writeJSON(w, 200, map[string]any{"ok": true, "subs": a.Subs.List(), "active_id": active, "notices": a.notices, "errors": errs})
}

// hSubsReorder 调整订阅卡片顺序。列表顺序即「自动切换订阅」的备选优先级：靠上的先被尝试。
func (a *App) hSubsReorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	b, _ := readBody(r, 1<<20)
	if err := json.Unmarshal(b, &body); err != nil || len(body.IDs) == 0 {
		writeErr(w, 400, "缺少 ids（订阅顺序）")
		return
	}
	if err := a.Subs.Reorder(body.IDs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "subs": a.Subs.List()})
}

func (a *App) hSubUpdateAll(w http.ResponseWriter, r *http.Request) {
	_, errs, err := a.Refresh(nil, true)
	code := 200
	if err != nil {
		code = 500
	}
	writeJSON(w, code, map[string]any{"errors": errs, "subs": a.Subs.List(), "notices": a.notices})
}

func (a *App) hSubImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Path    string `json:"path"`
	}
	b, _ := readBody(r, 20<<20)
	if err := json.Unmarshal(b, &body); err != nil {
		writeErr(w, 400, "请求体解析失败")
		return
	}
	content := []byte(body.Content)
	if len(content) == 0 && body.Path != "" {
		// 前端通过 @trimjs/web-app pickFile 选择文件后，把已授权的路径交给后端读取
		data, err := readUserPickedFile(body.Path)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		content = data
	}
	if len(content) == 0 {
		writeErr(w, 400, "缺少 content（订阅文件内容）或 path（已授权文件路径）")
		return
	}
	sub, err := a.ImportContent(body.Name, content)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.activateIfNone(sub.ID) // 列表中还没有激活订阅时，新导入的订阅自动激活
	// 记录来源文件路径（订阅页「来源」一栏展示），避免导入后无法追溯来源。
	if body.Path != "" {
		if s2, err := a.Subs.Update(sub.ID, func(x *Subscription) { x.Path = body.Path }); err == nil {
			sub = s2
		}
	}
	_, _, _ = a.Refresh([]string{}, true)
	writeJSON(w, 200, map[string]any{"sub": sub, "subs": a.Subs.List()})
}

// readUserPickedFile 读取用户在文件管理器中授权给本应用的文件（订阅导入用）。
// 安全边界：必须是绝对路径、常规文件、大小受限，且位于用户/共享存储范围内。
func readUserPickedFile(p string) ([]byte, error) {
	if p == "" || !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("文件路径无效")
	}
	clean := path.Clean(p)
	if clean == "/" || strings.Contains(clean, "..") {
		return nil, fmt.Errorf("文件路径无效")
	}
	// 用户存储卷在 fnOS 上为 /vol1、/vol2 ...（并不存在 /vol 本身），
	// 因此这里同时接受 /vol<数字>/... 形式，避免误拒共享目录中的文件。
	allowed := isVolPath(clean)
	if !allowed {
		for _, root := range []string{"/vol", "/home", "/var/apps", "/mnt", "/media"} {
			if clean == root || strings.HasPrefix(clean, root+"/") {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return nil, fmt.Errorf("仅支持导入用户存储（/vol1、/home、共享目录）中的文件")
	}
	ext := strings.ToLower(path.Ext(clean))
	switch ext {
	case ".yaml", ".yml", ".txt", ".conf", ".json", ".list", ".ini", ".base64", ".data":
	default:
		return nil, fmt.Errorf("不支持的文件类型：%s", ext)
	}
	st, err := os.Stat(clean)
	if err != nil {
		return nil, fmt.Errorf("无法读取文件：%v", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("不是常规文件")
	}
	if st.Size() > 8<<20 {
		return nil, fmt.Errorf("文件过大（超过 8 MiB）")
	}
	return os.ReadFile(clean)
}


// isVolPath 判断路径是否位于用户存储卷内（/vol1/xxx、/vol12/xxx 等）。
func isVolPath(clean string) bool {
	if !strings.HasPrefix(clean, "/vol") {
		return false
	}
	rest := clean[4:]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(rest) || rest[i] != '/' {
		return false
	}
	return len(rest) > i+1
}

func (a *App) hRebuild(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	res, err := a.rebuildLocked(nil)
	if err == nil && a.Mihomo.Running() {
		if e := a.Mihomo.Reload(); e != nil {
			err = e
		}
	}
	a.mu.Unlock()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "total": res.Total, "unique": res.Unique,
		"duplicated": res.Duplicated, "groups": res.Groups,
		"rules": res.Rules, "notices": res.Dropped,
	})
}

// ---------- 分流规则（设置页可手动更新，无需升级应用） ----------

func (a *App) hRulesInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, RulesInfo(a.Paths.Var))
}

func (a *App) hRulesUpdate(w http.ResponseWriter, r *http.Request) {
	s := loadSettings()
	proxyAddr := ""
	if a.Mihomo.Running() {
		proxyAddr = "127.0.0.1:" + strconv.Itoa(s.ProxyPort) // 直连 GitHub 失败时走本机代理
	}
	a.mu.Lock()
	meta, err := UpdateRules(a.Paths.Var, proxyAddr)
	if err == nil {
		_, err = a.rebuildLocked(nil) // 规则变更后重建配置，触发内核重新读取规则文件
	}
	if err == nil && a.Mihomo.Running() {
		if e := a.Mihomo.Reload(); e != nil {
			err = e
		}
	}
	a.mu.Unlock()
	if err != nil {
		writeErr(w, 500, "规则更新失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "version": rulesVersionLabel(meta), "updated_at": meta.UpdatedAt,
		"source": meta.Source, "files": meta.Files, "total": sumCounts(meta.Files),
	})
}

func (a *App) hConfigText(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(a.Paths.ConfigFile())
	if err != nil {
		writeErr(w, 404, "配置文件不存在")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// ---------- 设置 ----------

func (a *App) hSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, loadSettings())
}

func (a *App) hSettingsSet(w http.ResponseWriter, r *http.Request) {
	cur := loadSettings()
	var body struct {
		ProxyPort         *int    `json:"proxy_port"`
		AutoUpdateEnabled *bool   `json:"auto_update_enabled"`
		AutoUpdateHours   *int    `json:"auto_update_hours"`
		ProbeURL          *string `json:"probe_url"`
		AutoSwitch        *bool   `json:"auto_switch"`
	}
	b, _ := readBody(r, 1<<16)
	if err := json.Unmarshal(b, &body); err != nil {
		writeErr(w, 400, "请求体不是合法 JSON")
		return
	}
	portChanged := false
	if body.ProxyPort != nil {
		if *body.ProxyPort < 1 || *body.ProxyPort > 65535 {
			writeErr(w, 400, "端口必须在 1-65535 之间")
			return
		}
		if *body.ProxyPort != cur.ProxyPort {
			portChanged = true
		}
		cur.ProxyPort = *body.ProxyPort
	}
	if body.AutoUpdateEnabled != nil {
		cur.AutoUpdateEnabled = *body.AutoUpdateEnabled
	}
	if body.AutoUpdateHours != nil && *body.AutoUpdateHours > 0 {
		cur.AutoUpdateHours = *body.AutoUpdateHours
	}
	if body.ProbeURL != nil {
		u := strings.TrimSpace(*body.ProbeURL)
		if u == "" {
			u = DefaultProbeURL
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, 400, "测速链接需以 http:// 或 https:// 开头")
			return
		}
		cur.ProbeURL = u
	}
	if body.AutoSwitch != nil {
		cur.AutoSwitch = *body.AutoSwitch
	}
	if err := saveSettings(cur); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	msg := "设置已保存"
	if portChanged {
		a.mu.Lock()
		_, err := a.rebuildLocked(nil)
		if err == nil && a.Mihomo.Running() {
			err = a.Mihomo.Reload()
		}
		a.mu.Unlock()
		if err != nil {
			writeErr(w, 500, "端口已保存但应用失败："+err.Error())
			return
		}
		msg = "端口已修改并生效，请同步更新使用该代理的客户端"
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg, "settings": cur})
}
