package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type App struct {
	Paths  *Paths
	Mihomo *Mihomo
	Subs   *SubStore

	mu       sync.Mutex // 串行化「更新 / 重建配置」
	notices  []string
	updating bool
	lastErr  string

	// 自动切换订阅（设置页开关）的运行状态
	swMu       sync.Mutex
	swBusy     bool
	swAt       int64 // 上次自动切换（含尝试）的时间
	swLastFail bool  // 上次尝试是否以「所有订阅都不可用」结束
	swInfo     string
	swInfoAt   int64
	swInfoErr  bool
	swCheckAt  int64 // 上次实际检测（探测）的时间，用于按用户配置的间隔限流
}

func NewApp(p *Paths) *App {
	return &App{Paths: p, Mihomo: NewMihomo(), Subs: newSubStore()}
}

func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }
func nowUnix() int64 { return time.Now().Unix() }

// ---------- 配置生成 ----------

// rebuildLocked 依据所有启用的订阅重建 config.yaml。调用方需持有 a.mu。
func (a *App) rebuildLocked(onlyIDs map[string]bool) (*MergeResult, error) {
	subs := a.Subs.List()
	var all []map[string]any
	var groups []any
	var rules []string
	res := &MergeResult{}

	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		if onlyIDs != nil && !onlyIDs[sub.ID] {
			continue
		}
		raw, err := os.ReadFile(a.cacheFile(sub.ID))
		if err != nil {
			res.Dropped = append(res.Dropped, fmt.Sprintf("订阅「%s」暂无本地缓存，请先更新", sub.Name))
			continue
		}
		parsed, err := ParseSubscription(raw)
		if err != nil {
			res.Dropped = append(res.Dropped, fmt.Sprintf("订阅「%s」解析失败：%v", sub.Name, err))
			continue
		}
		for _, p := range parsed.Proxies {
			p["_sub"] = sub.ID
			all = append(all, p)
		}
		if len(groups) == 0 && len(parsed.Groups) > 0 {
			groups = parsed.Groups
		}
		if len(rules) == 0 && len(parsed.Rules) > 0 {
			rules = parsed.Rules
		}
	}

	res.Total = len(all)
	unique, dup := Dedupe(all)
	res.Unique = len(unique)
	res.Duplicated = dup
	for _, p := range unique {
		delete(p, "_sub")
	}
	groups = SanitizeGroups(groups, unique)
	exit := ExitGroupName(groups)
	var notices []string
	rules, notices = SanitizeRules(rules, exit)
	if len(unique) == 0 {
		groups = []any{map[string]any{"name": "PROXY", "type": "select", "proxies": []any{"DIRECT"}}}
		rules = []string{"MATCH,PROXY"}
		notices = append(notices, "当前没有可用节点，已生成直连（DIRECT）配置")
	}
	res.Groups, res.Rules = len(groups), len(rules)

	settings := loadSettings()
	ruleDir, err := EnsureRuleFiles(a.Paths.Var)
	if err != nil {
		notices = append(notices, "内置分流规则集写入失败："+err.Error())
	}
	res.Dropped = append(res.Dropped, notices...)
	cfg := BuildConfig(unique, groups, rules, settings.ProxyPort, a.Paths.MihomoSock(), ruleDir)
	if err := atomicWrite(a.Paths.ConfigFile(), cfg, 0o600); err != nil {
		return res, err
	}
	res.Config = cfg
	a.notices = res.Dropped
	return res, nil
}

// RebuildAndReload 重建配置；内核运行中则让其重载，立即生效。
func (a *App) RebuildAndReload() (*MergeResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	res, err := a.rebuildLocked(nil)
	if err != nil {
		return res, err
	}
	if a.Mihomo.Running() {
		if e := a.Mihomo.Reload(); e != nil {
			return res, e
		}
	}
	return res, nil
}

// activateIfNone 若当前没有任何订阅处于激活状态，则激活给定订阅（新增第一个订阅时自动生效）。
func (a *App) activateIfNone(id string) {
	if a.Subs.Active() != nil {
		return
	}
	_, _ = a.Subs.Activate(id, true)
}

func (a *App) cacheFile(id string) string {
	return a.Paths.Var + "/sub-" + id + ".cache"
}

// UpdateSubscription 拉取单个订阅并落地缓存。
func (a *App) UpdateSubscription(id string) (*Subscription, error) {
	sub := a.Subs.Get(id)
	if sub == nil {
		return nil, os.ErrNotExist
	}
	if sub.URL == "" {
		return nil, fmt.Errorf("该订阅没有下载地址（本地导入）")
	}
	fr, err := FetchSubscription(sub.URL)
	if err != nil {
		a.Subs.Update(id, func(s *Subscription) { s.LastError = err.Error() })
		return nil, err
	}
	parsed, err := ParseSubscription(fr.Body)
	if err != nil {
		a.Subs.Update(id, func(s *Subscription) { s.LastError = err.Error() })
		return nil, err
	}
	if err := atomicWrite(a.cacheFile(id), fr.Body, 0o600); err != nil {
		return nil, err
	}
	return a.Subs.Update(id, func(s *Subscription) {
		s.LastError = ""
		s.UpdatedAt = nowUnix()
		s.NodeCount = len(parsed.Proxies)
		s.Info = fr.Info
	})
}

// ImportContent 以本地文件内容创建订阅（不依赖网络）。
func (a *App) ImportContent(name string, content []byte) (*Subscription, error) {
	parsed, err := ParseSubscription(content)
	if err != nil {
		return nil, err
	}
	sub, err := a.Subs.Add(name, "")
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(a.cacheFile(sub.ID), content, 0o600); err != nil {
		return nil, err
	}
	return a.Subs.Update(sub.ID, func(s *Subscription) {
		s.UpdatedAt = nowUnix()
		s.NodeCount = len(parsed.Proxies)
	})
}

// Refresh 更新指定（或全部）订阅后重建配置；若内核在运行则热重载。
func (a *App) Refresh(ids []string, rebuild bool) (*MergeResult, []string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updating = true
	defer func() { a.updating = false }()

	var errs []string
	if ids == nil {
		for _, s := range a.Subs.List() {
			if s.URL != "" {
				ids = append(ids, s.ID)
			}
		}
	}
	for _, id := range ids {
		if _, err := a.UpdateSubscription(id); err != nil {
			sub := a.Subs.Get(id)
			nm := id
			if sub != nil {
				nm = sub.Name
			}
			errs = append(errs, fmt.Sprintf("%s：%v", nm, err))
		} else {
			_ = rebuild
		}
	}
	res, err := a.rebuildLocked(nil)
	if err != nil {
		return res, errs, err
	}
	if a.Mihomo.Running() {
		if e := a.Mihomo.Reload(); e != nil {
			errs = append(errs, "内核重载失败："+e.Error())
		}
	}
	settings := loadSettings()
	settings.LastUpdate = nowUnix()
	_ = saveSettings(settings)
	return res, errs, nil
}

// ---------- 总开关 ----------

func (a *App) PowerState() bool { return a.Mihomo.Running() }

func (a *App) SetPower(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !on {
		_ = atomicWrite(a.Paths.PowerFile(), []byte("off\n"), 0o600)
		return a.Mihomo.Stop()
	}
	if _, err := os.Stat(a.Paths.ConfigFile()); err != nil {
		if _, err := a.rebuildLocked(nil); err != nil {
			return err
		}
	}
	if err := a.Mihomo.Start(a.Paths.ConfigFile()); err != nil {
		return err
	}
	return atomicWrite(a.Paths.PowerFile(), []byte("on\n"), 0o600)
}

// OnStartup 系统启动/应用启动时调用：总开关保持关闭（文档要求）。
func (a *App) OnStartup() {
	// 旧数据迁移：历史版本允许多个订阅同时启用，现在只保留列表中最靠前的一个激活订阅
	if dropped, err := a.Subs.Normalize(); err == nil && len(dropped) > 0 {
		a.notices = append(a.notices, "同一时间只允许一个订阅处于激活状态，已自动取消激活："+strings.Join(dropped, "、"))
	}
	b, err := os.ReadFile(a.Paths.PowerFile())
	if err == nil && strings.TrimSpace(string(b)) == "on" && !a.Mihomo.Running() {
		// 上次为开启但进程已随重启消失：状态回落为关闭
		_ = atomicWrite(a.Paths.PowerFile(), []byte("off\n"), 0o600)
	}
}

// AutoLoop 订阅自动更新。
func (a *App) AutoLoop(stop <-chan struct{}) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	// 自动切换订阅的检测节拍：每 5 秒醒一次，真正的间隔由设置页的「检测间隔」决定
	swtick := time.NewTicker(5 * time.Second)
	defer swtick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-swtick.C:
			a.autoSwitchTick()
		case <-tick.C:
			s := loadSettings()
			if !s.AutoUpdateEnabled {
				continue
			}
			if s.LastUpdate > 0 && nowUnix()-s.LastUpdate < int64(s.AutoUpdateHours)*3600 {
				continue
			}
			same := false
			for _, sub := range a.Subs.List() {
				if sub.URL != "" {
					same = true
					break
				}
			}
			if !same {
				continue
			}
			if _, _, err := a.Refresh(nil, true); err != nil {
				// 失败静默记录，下个周期重试
				a.lastErr = err.Error()
			}
		}
	}
}

// ---------- 配置读取（内核未运行时前端仍可展示节点） ----------

type nodeInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (a *App) ConfigNodes() ([]nodeInfo, []string) {
	b, err := os.ReadFile(a.Paths.ConfigFile())
	if err != nil {
		return nil, nil
	}
	var root map[string]any
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, nil
	}
	out := []nodeInfo{}
	if ps, ok := root["proxies"].([]any); ok {
		for _, it := range ps {
			if m, ok := it.(map[string]any); ok {
				out = append(out, nodeInfo{Name: fmt.Sprint(m["name"]), Type: fmt.Sprint(m["type"])})
			}
		}
	}
	groups := []string{}
	if gs, ok := root["proxy-groups"].([]any); ok {
		for _, it := range gs {
			if m, ok := it.(map[string]any); ok {
				groups = append(groups, fmt.Sprint(m["name"]))
			}
		}
	}
	return out, groups
}

func (a *App) cacheFiles() []string {
	ents, err := os.ReadDir(a.Paths.Var)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "sub-") && strings.HasSuffix(e.Name(), ".cache") {
			out = append(out, e.Name())
		}
	}
	return out
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
