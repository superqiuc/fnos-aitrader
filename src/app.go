package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const appVersion = "1.1.0"

type LogBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	f     *os.File
}

func NewLogBuffer(path string, max int) *LogBuffer {
	lb := &LogBuffer{max: max}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		lb.f = f
	}
	return lb
}

func (l *LogBuffer) Printf(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05") + " " + fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
	if l.f != nil {
		_, _ = l.f.WriteString(line + "\n")
	}
}

func (l *LogBuffer) Tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.lines) {
		n = len(l.lines)
	}
	return append([]string{}, l.lines[len(l.lines)-n:]...)
}

type persisted struct {
	Accounts  map[string]*Account `json:"accounts"`
	Orders    []*Order            `json:"orders"`
	Decisions []Decision          `json:"decisions"`
	SavedAt   int64               `json:"saved_at"`
}

type App struct {
	appDir  string
	dataDir string
	listen  string

	store  *SettingsStore
	market *Market
	broker *Broker
	logs   *LogBuffer

	mu        sync.RWMutex
	decisions []Decision
	running   atomic.Bool
	startedAt time.Time

	ctx     context.Context
	cancel  context.CancelFunc
	httpSrv *http.Server

	autoMu   sync.Mutex
	autoLast map[string]int64
}

func NewApp(appDir, dataDir, listen string) (*App, error) {
	absApp, err := filepath.Abs(appDir)
	if err != nil {
		return nil, err
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absData, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%v", err)
	}
	store, err := NewSettingsStore(filepath.Join(absData, "config.json"))
	if err != nil {
		return nil, err
	}
	s := store.Get()
	if listen == "" {
		listen = s.Listen
	}
	a := &App{
		appDir: absApp, dataDir: absData, listen: strings.TrimPrefix(listen, ":"),
		store: store, market: NewMarket(marketProxy(s)), broker: NewBroker(s.InitialCash),
		logs:      NewLogBuffer(filepath.Join(absData, "engine.log"), 500),
		startedAt: time.Now(), autoLast: map[string]int64{},
	}
	a.loadState()
	return a, nil
}

func (a *App) logf(format string, args ...any) { a.logs.Printf(format, args...) }

func (a *App) statePath() string { return filepath.Join(a.dataDir, "state.json") }

func (a *App) loadState() {
	raw, err := os.ReadFile(a.statePath())
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		a.logf("状态读取失败：%v", err)
		return
	}
	if p.Accounts != nil {
		a.broker.Import(p.Accounts)
	}
	if p.Orders != nil {
		a.broker.ImportOrders(p.Orders)
	}
	a.mu.Lock()
	a.decisions = p.Decisions
	a.mu.Unlock()
	a.logf("已恢复 %d 条决策、%d 笔订单", len(p.Decisions), len(p.Orders))
}

func (a *App) persist() {
	a.mu.RLock()
	list := append([]Decision{}, a.decisions...)
	a.mu.RUnlock()
	p := persisted{Accounts: a.broker.Export(), Orders: a.broker.ExportOrders(), Decisions: list, SavedAt: time.Now().Unix()}
	buf, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := writeFileAtomic(a.statePath(), buf, 0o600); err != nil {
		a.logf("保存状态失败：%v", err)
	}
}

func (a *App) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.ctx, a.cancel = ctx, cancel
	a.logf("智能交易已启动（版本 %s，模拟盘）", appVersion)
	go a.autoLoop()
	go a.priceLoop()
	go func() {
		select {
		case <-time.After(20 * time.Second):
			a.scoreAll(a.ctx)
		case <-a.ctx.Done():
		}
	}()
}

func (a *App) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.httpSrv != nil {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = a.httpSrv.Shutdown(c)
		cancel()
	}
	a.persist()
}

func (a *App) Run() error {
	addr := ":" + a.listen
	srv := &http.Server{Addr: addr, Handler: a, ReadHeaderTimeout: 15 * time.Second}
	a.httpSrv = srv
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败：%v", addr, err)
	}
	a.logf("Web 控制台已监听 %s", addr)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-a.ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) autoLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			cfg := a.store.Get()
			if !cfg.Auto.Enabled || cfg.KillSwitch {
				continue
			}
			ctx, cancel := context.WithTimeout(a.ctx, 300*time.Second)
			a.runAutoCycle(ctx, false)
			cancel()
		}
	}
}

// autoIntervalFor 按标的给出自动分析周期：A 股固定日线。
func autoIntervalFor(raw, fallback string) string {
	if sym, err := ParseSymbol(raw); err == nil && sym.Venue == "ashare" {
		return "1d"
	}
	if fallback == "" {
		return "4h"
	}
	return fallback
}

// runAutoCycle 对到期的自动标的逐个分析，再按「信号强弱」排序执行。
// force=true 时忽略各标的自己的时间表（手动触发）。
func (a *App) runAutoCycle(ctx context.Context, force bool) []Decision {
	cfg := a.store.Get()
	targets := cfg.Auto.Targets
	if len(targets) == 0 {
		if cfg.DefaultSymbol == "" {
			a.logf("未配置自动标的，跳过本轮")
			return nil
		}
		targets = []WatchTarget{{Symbol: cfg.DefaultSymbol}}
	}
	now := time.Now().Unix()
	decisions := make([]Decision, 0, len(targets))
	for _, t := range targets {
		hours := t.EveryHours
		if hours <= 0 {
			hours = 6
		}
		if !force {
			a.autoMu.Lock()
			last := a.autoLast[t.Symbol]
			a.autoMu.Unlock()
			if last > 0 && now-last < int64(hours)*3600 {
				continue
			}
		}
		interval := t.Interval
		if interval == "" {
			interval = autoIntervalFor(t.Symbol, "4h")
		}
		d, err := a.runAgent(ctx, AgentRequest{Symbol: t.Symbol, Interval: interval}, modePlan)
		if err != nil {
			a.logf("自动分析 %s 失败：%v", t.Symbol, err)
			continue
		}
		a.autoMu.Lock()
		if a.autoLast == nil {
			a.autoLast = map[string]int64{}
		}
		a.autoLast[t.Symbol] = now
		a.autoMu.Unlock()
		decisions = append(decisions, *d)
	}

	// 先处理平仓（SELL），降低风险不受名额限制
	for i := range decisions {
		d := &decisions[i]
		if d.Action != "SELL" || d.Confidence < cfg.Auto.MinConfidence {
			continue
		}
		order, err := a.executeDecision(d, "自动平仓")
		a.markDecision(d.ID, order, err)
		if err != nil {
			d.ExecNote = "未执行：" + err.Error()
			a.logf("自动平仓 %s 跳过：%v", d.Symbol, err)
		} else {
			d.Executed = true
			d.OrderID = order.ID
			d.ExecNote = fmt.Sprintf("%s %.6f @ %.4f", order.Side, order.Qty, order.Price)
		}
	}

	// 开仓按置信度排序，受最大持仓数约束
	type cand struct {
		idx  int
		conf float64
	}
	var buys []cand
	for i := range decisions {
		if decisions[i].Action == "BUY" && decisions[i].Confidence >= cfg.Auto.MinConfidence {
			buys = append(buys, cand{i, decisions[i].Confidence})
		}
	}
	sort.Slice(buys, func(i, j int) bool { return buys[i].conf > buys[j].conf })
	for _, c := range buys {
		d := &decisions[c.idx]
		order, err := a.executeDecision(d, "自主交易")
		a.markDecision(d.ID, order, err)
		if err != nil {
			d.ExecNote = "未执行：" + err.Error()
			a.logf("自动开仓 %s 跳过：%v", d.Symbol, err)
		} else {
			d.Executed = true
			d.OrderID = order.ID
			d.ExecNote = fmt.Sprintf("%s %.6f @ %.4f", order.Side, order.Qty, order.Price)
			a.logf("自动开仓 %s（置信度 %.0f%%）", d.Symbol, d.Confidence*100)
		}
	}
	return decisions
}

// priceLoop 每 30 秒刷新一次持仓现价，保证持仓页净值/浮盈跟着行情走。
func (a *App) priceLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			symbols := map[string]bool{}
			for _, m := range []string{"crypto", "ashare"} {
				acct := a.broker.Account(m)
				if acct == nil {
					continue
				}
				for sym := range acct.Positions {
					symbols[sym] = true
				}
			}
			for raw := range symbols {
				sym, err := ParseSymbol(raw)
				if err != nil {
					continue
				}
				ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
				price, err := a.market.Quote(ctx, sym)
				cancel()
				if err != nil || price <= 0 {
					continue
				}
				a.broker.Mark(raw, price)
			}
		}
	}
}

func (a *App) lastDecisionAt() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.decisions) == 0 {
		return 0
	}
	return a.decisions[0].CreatedAt
}

func (a *App) Settings() Settings { return a.store.Get() }
