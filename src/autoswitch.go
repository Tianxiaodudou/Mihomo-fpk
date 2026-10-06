package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// 自动切换订阅：当「当前激活订阅」的全部节点都探测失败（无法正常代理）时，
// 按订阅列表顺序（卡片顺序，靠上的优先）依次尝试其余订阅，
// 切换到第一个存在可用节点的订阅；若全部不可用则回到原订阅并进入较长冷却。
//
// 依赖内核的延迟探测接口 /proxies/{name}/delay（内核未运行时本功能不工作）。
const (
	autoSwitchProbeCon = 8 // 并发探测数（固定值）
)

// 便于单元测试替换的探测钩子。
var (
	// probeAlive 探测单个节点是否可用（返回延迟、是否可用）。
	probeAlive = func(a *App, name, probeURL string, to time.Duration) (int, bool) {
		q := url.Values{}
		q.Set("timeout", fmt.Sprint(int(to/time.Millisecond)))
		if probeURL != "" {
			q.Set("url", probeURL)
		}
		b, code, err := a.Mihomo.API(http.MethodGet, "/proxies/"+url.PathEscape(name)+"/delay?"+q.Encode(), nil)
		if err != nil || code != 200 {
			return 0, false
		}
		var r struct {
			Delay int `json:"delay"`
		}
		if json.Unmarshal(b, &r) != nil || r.Delay <= 0 {
			return 0, false
		}
		return r.Delay, true
	}
)

// decideSwitch 纯决策函数：给定当前订阅下标 cur（共 n 个）与「第 i 个订阅是否可用」的判断，
// 返回应切换到的订阅下标；找不到可用订阅时返回 -1。
// 顺序从当前订阅的下一个开始，绕列表一圈，因此越靠上的订阅越早被尝试。
func decideSwitch(cur, n int, usable func(i int) bool) int {
	if n <= 1 || cur < 0 || cur >= n {
		return -1
	}
	for k := 1; k <= n; k++ {
		i := (cur + k) % n
		if i == cur {
			continue
		}
		if usable(i) {
			return i
		}
	}
	return -1
}

// activeNodeNames 当前配置中的节点名（配置只包含激活订阅的节点）。
func (a *App) activeNodeNames() []string {
	nodes, _ := a.ConfigNodes()
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Name == "" || n.Name == "DIRECT" || n.Name == "REJECT" || n.Name == "REJECT-DROP" || n.Name == "PASS" {
			continue
		}
		out = append(out, n.Name)
	}
	return out
}

// anyAlive 并发探测一批节点，只要有任意一个可用就立即返回 true。
func (a *App) anyAlive(names []string, probeURL string, to time.Duration) bool {
	if len(names) == 0 {
		return false
	}
	var (
		mu    sync.Mutex
		alive bool
		stop  = make(chan struct{})
		wg    sync.WaitGroup
		sem   = make(chan struct{}, autoSwitchProbeCon)
		once  sync.Once
	)
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			select {
			case <-stop:
				return
			default:
			}
			if _, ok := probeAlive(a, n, probeURL, to); ok {
				mu.Lock()
				alive = true
				mu.Unlock()
				once.Do(func() { close(stop) })
			}
		}(name)
	}
	wg.Wait()
	return alive
}

// subUsable 判断某个订阅是否可用（更新缓存 → 重建配置 → 探测节点）。
func (a *App) subUsable(sub *Subscription, probeURL string, to time.Duration) bool {
	if _, err := os.Stat(a.cacheFile(sub.ID)); err != nil {
		// 备选订阅可能从未拉取过，先尝试在线更新一次
		if _, err := a.UpdateSubscription(sub.ID); err != nil {
			return false
		}
	}
	if _, err := a.RebuildAndReload(); err != nil {
		return false
	}
	return a.anyAlive(a.activeNodeNames(), probeURL, to)
}

// durText 把时长写成人话（整除分钟显示分钟，否则显示秒）。
func durText(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
	return fmt.Sprintf("%d 秒", int(d/time.Second))
}

// setSwitchInfo 记录最近一次自动切换的说明，供界面展示。
func (a *App) setSwitchInfo(msg string, isErr bool) {
	a.swMu.Lock()
	a.swInfo = msg
	a.swInfoErr = isErr
	a.swInfoAt = nowUnix()
	a.swMu.Unlock()
}

// markSwitch 记录一次切换尝试的时间与结果。
func (a *App) markSwitch(failed bool) {
	a.swMu.Lock()
	a.swAt = nowUnix()
	a.swLastFail = failed
	a.swMu.Unlock()
}

// AutoSwitchInfo 返回最近一次自动切换说明（文本、时间、是否失败）。
func (a *App) AutoSwitchInfo() (string, int64, bool) {
	a.swMu.Lock()
	defer a.swMu.Unlock()
	return a.swInfo, a.swInfoAt, a.swInfoErr
}

// autoSwitchTick 自动切换订阅的一次检测。
func (a *App) autoSwitchTick() {
	s := loadSettings()
	if !s.AutoSwitch {
		return
	}
	interval, probeTO, cooldown, failCool := s.SwitchParams()
	a.swMu.Lock()
	if a.swBusy {
		a.swMu.Unlock()
		return
	}
	// 真正的检测间隔由用户在设置页配置（默认 45 秒）；调用方节拍更密，这里自行限流
	if a.swCheckAt > 0 && nowUnix()-a.swCheckAt < int64(interval/time.Second) {
		a.swMu.Unlock()
		return
	}
	cd := int64(cooldown / time.Second)
	if a.swLastFail {
		cd = int64(failCool / time.Second)
	}
	if a.swAt > 0 && nowUnix()-a.swAt < cd {
		a.swMu.Unlock()
		return
	}
	a.swCheckAt = nowUnix()
	a.swBusy = true
	a.swMu.Unlock()
	defer func() {
		a.swMu.Lock()
		a.swBusy = false
		a.swMu.Unlock()
	}()

	if !a.Mihomo.Running() {
		return // 内核未运行时不检测（无法探测）
	}
	cur := a.Subs.Active()
	if cur == nil {
		return
	}
	names := a.activeNodeNames()
	if len(names) == 0 {
		return
	}
	// 当前订阅还有可用节点 → 什么都不做
	if a.anyAlive(names, s.ProbeURL, probeTO) {
		return
	}
	a.setSwitchInfo(fmt.Sprintf("检测到「%s」的全部节点均不可用，正在尝试切换到其它订阅…", cur.Name), false)

	subs := a.Subs.List()
	cidx := -1
	for i, x := range subs {
		if x.ID == cur.ID {
			cidx = i
			break
		}
	}
	if cidx < 0 {
		return
	}
	usable := func(i int) bool {
		if subs[i].ID == cur.ID {
			return false
		}
		if _, err := a.Subs.Activate(subs[i].ID, true); err != nil {
			return false
		}
		return a.subUsable(subs[i], s.ProbeURL, probeTO)
	}
	target := decideSwitch(cidx, len(subs), usable)
	if target < 0 {
		// 所有订阅都不可用：回到原订阅，进入较长冷却
		if _, err := a.Subs.Activate(cur.ID, true); err == nil {
			_, _ = a.RebuildAndReload()
		}
		a.setSwitchInfo(fmt.Sprintf("所有订阅的节点均不可用，已保持在「%s」（%s内不再重试）", cur.Name, durText(failCool)), true)
		a.markSwitch(true)
		return
	}
	a.setSwitchInfo(fmt.Sprintf("已自动切换订阅：「%s」→「%s」（原订阅节点全部超时）", cur.Name, subs[target].Name), false)
	a.markSwitch(false)
}
