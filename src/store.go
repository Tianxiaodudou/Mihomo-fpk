package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"sync"
	"time"
)

// ---------- 设置 ----------

// DefaultProbeURL 节点测速（探测）默认目标地址：返回 204 的轻量地址。
const DefaultProbeURL = "http://www.gstatic.com/generate_204"

type Settings struct {
	ProxyPort         int    `json:"proxy_port"`
	AutoUpdateEnabled bool   `json:"auto_update_enabled"`
	AutoUpdateHours   int    `json:"auto_update_hours"`
	LastUpdate        int64  `json:"last_update"`
	ProbeURL          string `json:"probe_url"`
	// AutoSwitch 自动切换订阅：当前激活订阅的节点全部不可用时，自动切换到下一个订阅（按订阅列表顺序）。
	AutoSwitch bool `json:"auto_switch"`
}

func defaultSettings() *Settings {
	return &Settings{ProxyPort: 7890, AutoUpdateEnabled: true, AutoUpdateHours: 6, ProbeURL: DefaultProbeURL}
}

// ---------- 订阅 ----------

type SubInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`
	Expire   int64 `json:"expire"`
}

type Subscription struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Path      string   `json:"path,omitempty"`
	Enabled   bool     `json:"enabled"`
	UpdatedAt int64    `json:"updated_at"`
	LastError string   `json:"last_error"`
	NodeCount int      `json:"node_count"`
	Info      *SubInfo `json:"info,omitempty"`
}

type SubStore struct {
	mu   sync.Mutex
	Subs []*Subscription `json:"subs"`
}

func newSubStore() *SubStore {
	s := &SubStore{}
	b, err := os.ReadFile(P.SubsFile())
	if err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Subs == nil {
		s.Subs = []*Subscription{}
	}
	return s
}

func (s *SubStore) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(P.SubsFile(), b, 0o600)
}

func (s *SubStore) List() []*Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Subscription, len(s.Subs))
	copy(out, s.Subs)
	return out
}

func (s *SubStore) Get(id string) *Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.Subs {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func newID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 10)
	for i := range b {
		b[i] = chars[r.Intn(len(chars))]
	}
	return string(b)
}

func (s *SubStore) Add(name, url string) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		name = url
	}
	sub := &Subscription{ID: newID(), Name: name, URL: url}
	s.Subs = append(s.Subs, sub)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubStore) Update(id string, fn func(*Subscription)) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.Subs {
		if x.ID == id {
			fn(x)
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
			return x, nil
		}
	}
	return nil, os.ErrNotExist
}

func (s *SubStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.Subs {
		if x.ID == id {
			s.Subs = append(s.Subs[:i], s.Subs[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

// Active 返回当前处于「激活」状态的订阅。同一时刻至多一个，列表顺序靠前者优先。
func (s *SubStore) Active() *Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.Subs {
		if x.Enabled {
			return x
		}
	}
	return nil
}

// Activate 激活 / 取消激活某个订阅。
// 同一时刻只允许一个订阅处于激活状态：激活某个订阅时，其余订阅会自动取消激活。
func (s *SubStore) Activate(id string, on bool) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target *Subscription
	for _, x := range s.Subs {
		if x.ID == id {
			target = x
		}
	}
	if target == nil {
		return nil, os.ErrNotExist
	}
	if on {
		for _, x := range s.Subs {
			x.Enabled = x.ID == id
		}
	} else {
		target.Enabled = false
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return target, nil
}

// Reorder 按给定 id 顺序重排订阅卡片；未列出的订阅按原相对顺序追加在后面。
// 列表顺序即「自动切换订阅」的备选优先级：靠上的先被尝试。
func (s *SubStore) Reorder(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := make(map[string]*Subscription, len(s.Subs))
	for _, x := range s.Subs {
		byID[x.ID] = x
	}
	out := make([]*Subscription, 0, len(s.Subs))
	used := make(map[string]bool, len(s.Subs))
	for _, id := range ids {
		if x, ok := byID[id]; ok && !used[id] {
			used[id] = true
			out = append(out, x)
		}
	}
	for _, x := range s.Subs {
		if !used[x.ID] {
			out = append(out, x)
		}
	}
	s.Subs = out
	return s.saveLocked()
}

// Normalize 旧数据迁移：历史上允许多个订阅同时启用，现在改为单一「激活」语义，
// 故只保留列表中最靠前的一个激活订阅，其余取消激活。返回被取消激活的订阅名。
func (s *SubStore) Normalize() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := false
	var dropped []string
	for _, x := range s.Subs {
		if !x.Enabled {
			continue
		}
		if !first {
			first = true
			continue
		}
		x.Enabled = false
		if x.Name != "" {
			dropped = append(dropped, x.Name)
		} else {
			dropped = append(dropped, x.ID)
		}
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	return dropped, s.saveLocked()
}

func (s *SubStore) saveLocked() error {
	b, err := json.MarshalIndent(struct {
		Subs []*Subscription `json:"subs"`
	}{s.Subs}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(P.SubsFile(), b, 0o600)
}

// ---------- 设置读写 ----------

var settingsMu sync.Mutex

func loadSettings() *Settings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	s := defaultSettings()
	b, err := os.ReadFile(P.SettingsFile())
	if err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.ProxyPort <= 0 || s.ProxyPort > 65535 {
		s.ProxyPort = 7890
	}
	if s.AutoUpdateHours <= 0 {
		s.AutoUpdateHours = 6
	}
	if s.ProbeURL == "" {
		s.ProbeURL = DefaultProbeURL
	}
	return s
}

func saveSettings(s *Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(P.SettingsFile(), b, 0o600)
}
