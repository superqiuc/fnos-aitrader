package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type LLMSettings struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
	UseProxy bool   `json:"use_proxy"`
}

type RiskSettings struct {
	MaxPositionPct float64 `json:"max_position_pct"`
	MaxOpenTrades  int     `json:"max_open_trades"`
	MinNotional    float64 `json:"min_notional"`
}

// WatchTarget 是自动交易的单标的配置；Interval/EveryHours 留空时用全局默认。
type WatchTarget struct {
	Symbol     string `json:"symbol"`
	Interval   string `json:"interval,omitempty"`
	EveryHours int    `json:"every_hours,omitempty"`
}

type AutoSettings struct {
	Enabled       bool          `json:"enabled"`
	EveryHours    int           `json:"every_hours"`
	Interval      string        `json:"interval"`
	MinConfidence float64       `json:"min_confidence"`
	ManualTrade   bool          `json:"manual_trade"`
	Watchlist     []string      `json:"watchlist,omitempty"`
	Targets       []WatchTarget `json:"targets,omitempty"`
}

type NewsSettings struct {
	Auto     bool `json:"auto"`
	MaxItems int  `json:"max_items"`
}

type Settings struct {
	Listen        string             `json:"listen"`
	ProxyURL      string             `json:"proxy_url"`
	LLM           LLMSettings        `json:"llm"`
	Risk          RiskSettings       `json:"risk"`
	News          NewsSettings       `json:"news"`
	Auto          AutoSettings       `json:"auto"`
	DefaultSymbol string             `json:"default_symbol"`
	InitialCash   map[string]float64 `json:"initial_cash"`
	SentimentPos  []string           `json:"sentiment_pos,omitempty"`
	SentimentNeg  []string           `json:"sentiment_neg,omitempty"`
	KillSwitch    bool               `json:"kill_switch"`
	// MarketProxy 控制行情请求是否走代理；nil 视为 true（兼容旧配置）。
	MarketProxy *bool `json:"market_proxy,omitempty"`
}

func (s Settings) UseMarketProxy() bool {
	if s.MarketProxy == nil {
		return true
	}
	return *s.MarketProxy
}

func marketProxy(s Settings) string {
	if s.UseMarketProxy() {
		return s.ProxyURL
	}
	return ""
}

func defaultSettings() Settings {
	return Settings{
		Listen:        "8906",
		DefaultSymbol: "binance:BTCUSDT",
		InitialCash:   map[string]float64{"crypto": 10000, "ashare": 100000},
		LLM:           LLMSettings{BaseURL: "https://api.deepseek.com", Model: "deepseek-flash"},
		Risk:          RiskSettings{MaxPositionPct: 0.25, MaxOpenTrades: 3, MinNotional: 10},
		News:          NewsSettings{Auto: true, MaxItems: 8},
		Auto:          AutoSettings{Enabled: false, EveryHours: 6, Interval: "4h", MinConfidence: 0.55},
	}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	if strings.TrimSpace(s.Listen) == "" {
		s.Listen = d.Listen
	}
	s.Listen = strings.TrimPrefix(s.Listen, ":")
	if s.DefaultSymbol == "" {
		s.DefaultSymbol = d.DefaultSymbol
	}
	if s.InitialCash == nil {
		s.InitialCash = d.InitialCash
	}
	for _, m := range []string{"crypto", "ashare"} {
		if s.InitialCash[m] <= 0 {
			s.InitialCash[m] = d.InitialCash[m]
		}
	}
	if s.Risk.MaxPositionPct <= 0 || s.Risk.MaxPositionPct > 1 {
		s.Risk.MaxPositionPct = d.Risk.MaxPositionPct
	}
	if s.Risk.MaxOpenTrades <= 0 || s.Risk.MaxOpenTrades > 20 {
		s.Risk.MaxOpenTrades = d.Risk.MaxOpenTrades
	}
	if s.News.MaxItems <= 0 || s.News.MaxItems > 20 {
		s.News.MaxItems = 8
	}
	if s.Risk.MinNotional <= 0 {
		s.Risk.MinNotional = d.Risk.MinNotional
	}
	if s.Auto.EveryHours <= 0 || s.Auto.EveryHours > 48 {
		s.Auto.EveryHours = d.Auto.EveryHours
	}
	clean := []string{}
	seen := map[string]bool{}
	for _, raw := range s.Auto.Watchlist {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			continue
		}
		if _, err := ParseSymbol(raw); err != nil {
			continue
		}
		seen[raw] = true
		clean = append(clean, raw)
		if len(clean) >= 10 {
			break
		}
	}
	s.Auto.Watchlist = clean
	// 兼容旧配置：只有 watchlist 时按全局默认生成 targets
	if len(s.Auto.Targets) == 0 && len(clean) > 0 {
		for _, raw := range clean {
			s.Auto.Targets = append(s.Auto.Targets, WatchTarget{Symbol: raw})
		}
	}
	cleanedTargets := []WatchTarget{}
	seenTarget := map[string]bool{}
	for _, t := range s.Auto.Targets {
		t.Symbol = strings.TrimSpace(t.Symbol)
		if t.Symbol == "" || seenTarget[t.Symbol] {
			continue
		}
		sym, err := ParseSymbol(t.Symbol)
		if err != nil {
			continue
		}
		if t.Interval == "" || !supportedIntervals[t.Interval] {
			t.Interval = "4h"
		}
		if sym.Venue == "ashare" {
			t.Interval = "1d"
		}
		if t.EveryHours <= 0 || t.EveryHours > 168 {
			t.EveryHours = 6
		}
		seenTarget[t.Symbol] = true
		cleanedTargets = append(cleanedTargets, t)
		if len(cleanedTargets) >= 10 {
			break
		}
	}
	s.Auto.Targets = cleanedTargets
	if len(s.Auto.Targets) > 0 {
		// 默认标的仅作为「空标的请求」的兜底，跟随第一个自动标的
		s.DefaultSymbol = s.Auto.Targets[0].Symbol
	}
	if s.Auto.Interval == "" || !supportedIntervals[s.Auto.Interval] {
		s.Auto.Interval = "4h"
	}
	if s.Auto.MinConfidence <= 0 || s.Auto.MinConfidence > 1 {
		s.Auto.MinConfidence = d.Auto.MinConfidence
	}
	if s.LLM.BaseURL == "" {
		s.LLM.BaseURL = d.LLM.BaseURL
	}
	if s.LLM.Model == "" {
		s.LLM.Model = d.LLM.Model
	}
	s.SentimentPos = cleanWords(s.SentimentPos)
	s.SentimentNeg = cleanWords(s.SentimentNeg)
}

func cleanWords(words []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" || len([]rune(w)) > 20 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

func (s *Settings) validate() error {
	if s.DefaultSymbol != "" {
		if _, err := ParseSymbol(s.DefaultSymbol); err != nil {
			return err
		}
	}
	if s.LLM.Enabled {
		if !strings.HasPrefix(s.LLM.BaseURL, "http://") && !strings.HasPrefix(s.LLM.BaseURL, "https://") {
			return errors.New("LLM 接口地址需以 http:// 或 https:// 开头")
		}
		if strings.TrimSpace(s.LLM.Model) == "" {
			return errors.New("LLM 模型名不能为空")
		}
	}
	return nil
}

type SettingsStore struct {
	mu   sync.RWMutex
	path string
	data Settings
}

func NewSettingsStore(path string) (*SettingsStore, error) {
	st := &SettingsStore{path: path, data: defaultSettings()}
	raw, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &st.data); err != nil {
			st.data = defaultSettings()
		}
	}
	st.data.normalize()
	if err := st.saveLocked(); err != nil {
		return nil, err
	}
	return st, nil
}

func (st *SettingsStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(st.data, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(st.path, buf, 0o600)
}

func (st *SettingsStore) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.data
}

func (st *SettingsStore) Update(fn func(*Settings) error) (Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cp := st.data
	if err := fn(&cp); err != nil {
		return st.data, err
	}
	cp.normalize()
	if err := cp.validate(); err != nil {
		return st.data, err
	}
	st.data = cp
	if err := st.saveLocked(); err != nil {
		return st.data, err
	}
	return st.data, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
