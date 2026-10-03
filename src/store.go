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
	sub := &Subscription{ID: newID(), Name: name, URL: url, Enabled: true}
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
