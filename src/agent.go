package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 类型

type AgentReport struct {
	Role    string   `json:"role"`
	Bias    string   `json:"bias"`
	Score   float64  `json:"score"`
	Summary string   `json:"summary"`
	Points  []string `json:"points"`
	Source  string   `json:"source"`
}

type DecisionScore struct {
	ScoredAt         int64   `json:"scored_at"`
	HorizonBars      int     `json:"horizon_bars"`
	EntryPrice       float64 `json:"entry_price"`
	ExitPrice        float64 `json:"exit_price"`
	ForwardReturnPct float64 `json:"forward_return_pct"`
	Correct          bool    `json:"correct"`
	Note             string  `json:"note"`
}

type Decision struct {
	ID        string        `json:"id"`
	Symbol    string        `json:"symbol"`
	Interval  string        `json:"interval"`
	CreatedAt int64         `json:"created_at"`
	Price     float64       `json:"price"`
	Reports   []AgentReport `json:"reports"`
	Bull      string        `json:"bull_case"`
	Bear      string        `json:"bear_case"`

	Action     string  `json:"action"`
	Target     float64 `json:"target"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
	Source     string  `json:"source"`

	NewsAuto  bool `json:"news_auto"`
	NewsCount int  `json:"news_count"`

	Executed bool   `json:"executed"`
	OrderID  string `json:"order_id,omitempty"`
	ExecNote string `json:"exec_note,omitempty"`
	Copied   bool   `json:"copied,omitempty"`

	Score *DecisionScore `json:"score,omitempty"`
}

type ScoreStats struct {
	Total   int     `json:"total"`
	Scored  int     `json:"scored"`
	Pending int     `json:"pending"`
	Correct int     `json:"correct"`
	HitRate float64 `json:"hit_rate_pct"`
	AvgRet  float64 `json:"avg_return_pct"`
}

// ---------------------------------------------------------------- LLM

func (a *App) llmJSON(ctx context.Context, system, user string, out any) error {
	return a.llmJSONWith(ctx, a.store.Get().LLM, system, user, out)
}

// llmJSONWith 用指定设置调用，便于「测试连接」在不保存的情况下验证参数。
func (a *App) llmJSONWith(ctx context.Context, s LLMSettings, system, user string, out any) error {
	if !s.Enabled {
		return fmt.Errorf("LLM 未启用")
	}
	payload := map[string]any{
		"model": s.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
	}
	client := &http.Client{Timeout: 60 * time.Second}
	cfg := a.store.Get()
	if s.UseProxy && strings.TrimSpace(cfg.ProxyURL) != "" {
		if u, err := url.Parse(cfg.ProxyURL); err == nil {
			client = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
		}
	}
	endpoint := strings.TrimRight(s.BaseURL, "/") + "/chat/completions"
	do := func(p map[string]any) ([]byte, int, error) {
		body, _ := json.Marshal(p)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return data, resp.StatusCode, nil
	}
	data, code, err := do(payload)
	if err != nil {
		return err
	}
	if code == http.StatusBadRequest {
		delete(payload, "response_format")
		data, code, err = do(payload)
		if err != nil {
			return err
		}
	}
	if code != http.StatusOK {
		return fmt.Errorf("LLM HTTP %d：%s", code, truncateStr(string(data), 160))
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Choices) == 0 {
		return fmt.Errorf("LLM 响应无法解析")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(strings.TrimSpace(content), "```")
	if err := json.Unmarshal([]byte(content), out); err != nil {
		if js, ok := extractJSON(content); ok {
			if err := json.Unmarshal([]byte(js), out); err == nil {
				return nil
			}
		}
		return fmt.Errorf("LLM 输出不是合法 JSON")
	}
	return nil
}

func extractJSON(s string) (string, bool) {
	start := strings.Index(s, "{")
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------- 分析师

func biasOf(score float64) string {
	switch {
	case score >= 0.2:
		return "bull"
	case score <= -0.2:
		return "bear"
	}
	return "neutral"
}

func summarize(prefix string, score float64) string {
	switch {
	case score >= 0.45:
		return prefix + "明显偏多"
	case score >= 0.2:
		return prefix + "偏多"
	case score <= -0.45:
		return prefix + "明显偏空"
	case score <= -0.2:
		return prefix + "偏空"
	}
	return prefix + "中性"
}

func technicalRules(s Snapshot) AgentReport {
	score := 0.0
	var points []string
	if s.MA20 > 0 && s.MA60 > 0 {
		switch {
		case s.Price > s.MA20 && s.MA20 > s.MA60:
			score += 0.45
			points = append(points, fmt.Sprintf("价格 %.4f 在 MA20/MA60 之上，多头排列", s.Price))
		case s.Price < s.MA20 && s.MA20 < s.MA60:
			score -= 0.45
			points = append(points, fmt.Sprintf("价格 %.4f 在 MA20/MA60 之下，空头排列", s.Price))
		default:
			points = append(points, "均线纠缠，趋势不明")
		}
	}
	switch {
	case s.RSI14 <= 30:
		score += 0.25
		points = append(points, fmt.Sprintf("RSI %.1f 超卖", s.RSI14))
	case s.RSI14 >= 70:
		score -= 0.25
		points = append(points, fmt.Sprintf("RSI %.1f 超买", s.RSI14))
	default:
		points = append(points, fmt.Sprintf("RSI %.1f 中性", s.RSI14))
	}
	if s.Return20 != 0 {
		if s.Return20 > 0 {
			score += 0.15
		} else {
			score -= 0.15
		}
		points = append(points, fmt.Sprintf("近20根 %.2f%%，近5根 %.2f%%", s.Return20, s.Return5))
	}
	score = clamp(score, -1, 1)
	return AgentReport{Role: "技术分析师", Bias: biasOf(score), Score: score, Summary: summarize("技术面", score), Points: points, Source: "rules"}
}

var posWords = []string{"上涨", "大涨", "涨停", "利好", "突破", "回购", "降息", "增长", "盈利", "超预期", "上调", "增持", "净流入", "资金流入", "流入", "获批", "批准", "合作", "订单", "分红", "复苏", "回暖", "创新高", "牛市", "买入", "加仓", "中标", "rally", "surge", "upgrade", "inflow", "bullish", "beat"}
var negWords = []string{"下跌", "大跌", "跌停", "暴跌", "崩盘", "利空", "亏损", "退市", "违约", "制裁", "爆仓", "清算", "下调", "减持", "净流出", "资金流出", "调查", "罚款", "诉讼", "违规", "爆雷", "熊市", "卖出", "减仓", "失守", "裁员", "crash", "plunge", "downgrade", "lawsuit", "hack", "outflow", "bearish", "miss"}

func newsRules(news string, extraPos, extraNeg []string) AgentReport {
	low := strings.ToLower(news)
	type hit struct {
		w   string
		pos bool
	}
	var matched []hit
	seen := map[string]bool{}
	collect := func(words []string, positive bool) {
		for _, w := range words {
			w = strings.TrimSpace(w)
			if w == "" || seen[w] {
				continue
			}
			seen[w] = true
			if strings.Contains(low, strings.ToLower(w)) {
				matched = append(matched, hit{w, positive})
			}
		}
	}
	collect(posWords, true)
	collect(extraPos, true)
	collect(negWords, false)
	collect(extraNeg, false)
	sort.SliceStable(matched, func(i, j int) bool { return len(matched[i].w) > len(matched[j].w) })
	kept := []hit{}
	for _, h := range matched {
		overlap := false
		for _, k := range kept {
			if k.pos == h.pos && strings.Contains(k.w, h.w) {
				overlap = true
				break
			}
		}
		if !overlap {
			kept = append(kept, h)
		}
	}
	pos, neg := 0, 0
	var names []string
	for _, h := range kept {
		if h.pos {
			pos++
			names = append(names, "+"+h.w)
		} else {
			neg++
			names = append(names, "-"+h.w)
		}
	}
	score := 0.0
	if len(kept) > 0 {
		score = clamp(float64(pos-neg)/3, -1, 1)
	}
	points := []string{fmt.Sprintf("正面 %d 个，负面 %d 个", pos, neg)}
	if len(names) > 0 {
		points = append(points, "关键词："+strings.Join(names, "、"))
	}
	return AgentReport{Role: "新闻情绪分析师", Bias: biasOf(score), Score: score, Summary: summarize("消息面", score), Points: points, Source: "rules"}
}

func fundamentalsRules(f *Fundamentals) AgentReport {
	rep := AgentReport{Role: "基本面分析师", Bias: "neutral", Source: "rules"}
	if f == nil || len(f.Metrics) == 0 {
		rep.Summary = "无财报数据"
		rep.Points = []string{"加密资产没有财报，改用技术与消息面"}
		return rep
	}
	score := 0.0
	var points []string
	if pe := f.Metrics["pe_ttm"]; pe > 0 {
		switch {
		case pe <= 15:
			score += 0.25
			points = append(points, fmt.Sprintf("市盈率 %.1f 倍，估值偏低", pe))
		case pe <= 30:
			score += 0.05
			points = append(points, fmt.Sprintf("市盈率 %.1f 倍，估值合理", pe))
		case pe > 60:
			score -= 0.25
			points = append(points, fmt.Sprintf("市盈率 %.1f 倍，估值很高", pe))
		}
	}
	if pb := f.Metrics["pb"]; pb > 0 {
		if pb < 1 {
			score += 0.15
			points = append(points, fmt.Sprintf("市净率 %.2f，低于净资产", pb))
		} else if pb > 6 {
			score -= 0.15
			points = append(points, fmt.Sprintf("市净率 %.2f，偏高", pb))
		}
	}
	if m := f.Metrics["total_mcap_yi"]; m > 0 {
		points = append(points, fmt.Sprintf("总市值约 %.0f 亿元", m))
	}
	score = clamp(score, -1, 1)
	rep.Bias = biasOf(score)
	rep.Score = score
	rep.Summary = summarize("基本面", score)
	rep.Points = points
	return rep
}

// ---------------------------------------------------------------- 辩论与交易员

func debateRules(reports []AgentReport) (string, string) {
	sum := 0.0
	for _, r := range reports {
		sum += r.Score
	}
	if len(reports) > 0 {
		sum /= float64(len(reports))
	}
	bull := fmt.Sprintf("看多研究员：综合评分 %.2f。若趋势延续，风险收益比可以接受。", sum)
	bear := fmt.Sprintf("看空研究员：综合评分 %.2f。若消息或趋势反转，回撤会放大，应控制仓位。", sum)
	if sum > 0 {
		bull = fmt.Sprintf("看多研究员：综合评分 %.2f，多头证据占优，可在回撤可控时建仓。", sum)
	} else {
		bear = fmt.Sprintf("看空研究员：综合评分 %.2f，空头证据更充分，应回避追高。", sum)
	}
	return bull, bear
}

func traderRules(reports []AgentReport) (string, float64, float64, string) {
	score := 0.0
	for _, r := range reports {
		score += r.Score
	}
	if len(reports) > 0 {
		score /= float64(len(reports))
	}
	switch {
	case score >= 0.25:
		return "BUY", clamp(0.5+score*0.5, 0.3, 1), clamp(score, 0, 1), fmt.Sprintf("综合评分 %.2f 偏多", score)
	case score <= -0.25:
		return "SELL", 0, clamp(-score, 0, 1), fmt.Sprintf("综合评分 %.2f 偏空", score)
	}
	return "HOLD", 0, clamp(1-absFloat(score)*2, 0, 1), fmt.Sprintf("综合评分 %.2f 接近中性", score)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------- 流水线

type AgentRequest struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	News     string `json:"news"`
}

// checkTradeRisk 校验最大持仓数与最小下单额。
func (a *App) checkTradeRisk(d Decision) error {
	cfg := a.store.Get()
	sym, err := ParseSymbol(d.Symbol)
	if err != nil {
		return err
	}
	acct := a.broker.Account(sym.Market())
	if acct == nil {
		return fmt.Errorf("账户不可用")
	}
	equity := a.broker.Equity(sym.Market())
	curQty, curVal := 0.0, 0.0
	if p := acct.Positions[d.Symbol]; p != nil {
		curQty, curVal = p.Qty, p.Qty*p.Last
	}
	targetVal := d.Target * equity
	if d.Action == "SELL" {
		targetVal = 0
	}
	delta := targetVal - curVal
	if absFloat(delta) < cfg.Risk.MinNotional {
		return fmt.Errorf("变动金额 %.2f 低于最小下单额 %.2f", absFloat(delta), cfg.Risk.MinNotional)
	}
	if delta > 0 && curQty <= 0 && len(acct.Positions) >= cfg.Risk.MaxOpenTrades {
		return fmt.Errorf("已达最大持仓数 %d，拒绝新开仓", cfg.Risk.MaxOpenTrades)
	}
	return nil
}

// executeDecision 统一执行入口：先风控，再按目标仓位调仓。
func (a *App) executeDecision(d *Decision, source string) (*Order, error) {
	if a.store.Get().KillSwitch {
		return nil, fmt.Errorf("停机开关已开启")
	}
	if d.Action == "HOLD" {
		return nil, fmt.Errorf("HOLD 无需执行")
	}
	if err := a.checkTradeRisk(*d); err != nil {
		return nil, err
	}
	target := d.Target
	if d.Action == "SELL" {
		target = 0
	}
	return a.broker.Execute(d.Symbol, target, d.Price, source+"："+d.Rationale)
}

type runMode int

const (
	modeManual runMode = iota // 手动分析：按 manual_trade 决定是否下单
	modePlan                  // 只分析，不执行（自动轮询用，之后统一排序执行）
	modeAuto                  // 定时任务：直接执行
)

func (a *App) runAgent(ctx context.Context, req AgentRequest, mode runMode) (*Decision, error) {
	if !a.running.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("已有一次分析在进行")
	}
	defer a.running.Store(false)

	cfg := a.store.Get()
	sym, err := ParseSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	interval := req.Interval
	if interval == "" {
		interval = "4h"
	}
	if !supportedIntervals[interval] {
		return nil, fmt.Errorf("不支持的周期：%s（可选 1m/5m/15m/30m/1h/4h/1d）", interval)
	}
	if sym.Venue == "ashare" && interval == "4h" {
		return nil, fmt.Errorf("A 股暂不支持 4h，请改用 1h 或 1d")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	bars, err := a.market.Klines(fetchCtx, sym, interval, 300)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("行情获取失败：%v", err)
	}
	if len(bars) < 60 {
		return nil, fmt.Errorf("行情样本不足：%d 根", len(bars))
	}
	snap := buildSnapshot(sym.String(), bars)
	a.broker.Mark(sym.String(), snap.Price)

	d := &Decision{ID: fmt.Sprintf("d%d", time.Now().UnixNano()), Symbol: sym.String(), Interval: interval,
		CreatedAt: time.Now().Unix(), Price: snap.Price}

	// 新闻留空时自动抓取相关新闻
	newsText := req.News
	if strings.TrimSpace(newsText) == "" && cfg.News.Auto {
		nctx, ncancel := context.WithTimeout(ctx, 20*time.Second)
		if text, n, err := a.autoNews(nctx, sym, cfg.News.MaxItems); err == nil && n > 0 {
			newsText = "以下是自动抓取的相关新闻，请据此判断情绪：\n" + text
			d.NewsAuto = true
			d.NewsCount = n
			a.logf("自动抓取新闻 %d 条（%s）", n, sym.String())
		} else {
			a.logf("自动抓取新闻失败：%v", err)
		}
		ncancel()
	}

	// 分析师并行
	var wg sync.WaitGroup
	var tech, news, fund AgentReport
	wg.Add(3)
	go func() { defer wg.Done(); tech = a.technical(ctx, snap) }()
	go func() { defer wg.Done(); news = a.news(ctx, newsText) }()
	go func() {
		defer wg.Done()
		fctx, fcancel := context.WithTimeout(ctx, 25*time.Second)
		defer fcancel()
		f, err := a.fundamentals(fctx, sym)
		if err != nil {
			fund = AgentReport{Role: "基本面分析师", Bias: "neutral", Summary: "无可用财报数据", Points: []string{err.Error()}, Source: "rules"}
			return
		}
		fund = a.fundamentalReport(fctx, f)
	}()
	wg.Wait()
	d.Reports = []AgentReport{tech, news, fund}

	d.Bull, d.Bear = debateRules(d.Reports)
	action, target, conf, rationale := traderRules(d.Reports)
	d.Action, d.Target, d.Confidence, d.Rationale, d.Source = action, target, conf, rationale, "rules"

	if cfg.LLM.Enabled {
		if act, tgt, cf, why, ok := a.llmTrader(ctx, d); ok {
			d.Action, d.Target, d.Confidence, d.Rationale, d.Source = act, tgt, cf, why, "llm"
		}
	}

	if d.Target > cfg.Risk.MaxPositionPct {
		d.Target = cfg.Risk.MaxPositionPct
	}
	if d.Action == "HOLD" {
		d.Target = 0
	}

	// 自主交易：定时任务始终可执行；手动分析需显式允许
	allowTrade := mode == modeAuto || cfg.Auto.ManualTrade
	if mode == modePlan {
		// 自动轮询先只收集结论，由调度器统一排序执行
	} else if cfg.Auto.Enabled && !cfg.KillSwitch && d.Action != "HOLD" && d.Confidence >= cfg.Auto.MinConfidence {
		if !allowTrade {
			d.ExecNote = "手动分析：仅生成结论（设置里可开启「手动分析也自动下单」）"
		} else {
			order, err := a.executeDecision(d, "自主交易")
			if err != nil {
				d.ExecNote = "未执行：" + err.Error()
				a.logf("自主交易跳过 %s：%v", d.Symbol, err)
			} else {
				d.Executed = true
				d.OrderID = order.ID
				d.ExecNote = fmt.Sprintf("%s %.6f @ %.4f", order.Side, order.Qty, order.Price)
				a.logf("自主交易 %s %s %.6f @ %.4f", order.Side, order.Symbol, order.Qty, order.Price)
			}
		}
	}

	a.mu.Lock()
	a.decisions = append([]Decision{*d}, a.decisions...)
	if len(a.decisions) > 200 {
		a.decisions = a.decisions[:200]
	}
	a.mu.Unlock()
	a.persist()
	a.logf("分析完成 %s：%s（置信度 %.0f%%，%s）", d.Symbol, d.Action, d.Confidence*100, d.Source)
	return d, nil
}

func (a *App) technical(ctx context.Context, s Snapshot) AgentReport {
	base := technicalRules(s)
	cfg := a.store.Get()
	if !cfg.LLM.Enabled {
		return base
	}
	var out struct {
		Bias    string   `json:"bias"`
		Score   float64  `json:"score"`
		Summary string   `json:"summary"`
		Points  []string `json:"points"`
	}
	sys := `你是技术分析师。输出 JSON：{"bias":"bull|bear|neutral","score":-1到1,"summary":"一句话","points":["依据"]}。只输出 JSON。`
	user := fmt.Sprintf("标的 %s，现价 %.4f，MA20 %.4f，MA60 %.4f，RSI14 %.1f，ATR%% %.2f，近5根 %.2f%%，近20根 %.2f%%。",
		s.Symbol, s.Price, s.MA20, s.MA60, s.RSI14, s.ATRPct, s.Return5, s.Return20)
	if err := a.llmJSON(ctx, sys, user, &out); err != nil {
		base.Points = append(base.Points, "（LLM 失败，已回退规则）")
		return base
	}
	if out.Bias == "" {
		out.Bias = biasOf(out.Score)
	}
	return AgentReport{Role: "技术分析师", Bias: out.Bias, Score: clamp(out.Score, -1, 1), Summary: out.Summary, Points: out.Points, Source: "llm"}
}

func (a *App) news(ctx context.Context, news string) AgentReport {
	cfg := a.store.Get()
	base := newsRules(news, cfg.SentimentPos, cfg.SentimentNeg)
	if !cfg.LLM.Enabled || strings.TrimSpace(news) == "" {
		return base
	}
	var out struct {
		Bias    string   `json:"bias"`
		Score   float64  `json:"score"`
		Summary string   `json:"summary"`
		Points  []string `json:"points"`
	}
	sys := `你是新闻情绪分析师。判断新闻对标的短中期影响。输出 JSON：{"bias":"bull|bear|neutral","score":-1到1,"summary":"一句话","points":["依据"]}。只输出 JSON。`
	if err := a.llmJSON(ctx, sys, "新闻：\n"+truncateStr(news, 4000), &out); err != nil {
		base.Points = append(base.Points, "（LLM 失败，已回退规则）")
		return base
	}
	if out.Bias == "" {
		out.Bias = biasOf(out.Score)
	}
	return AgentReport{Role: "新闻情绪分析师", Bias: out.Bias, Score: clamp(out.Score, -1, 1), Summary: out.Summary, Points: out.Points, Source: "llm"}
}

func (a *App) fundamentalReport(ctx context.Context, f *Fundamentals) AgentReport {
	base := fundamentalsRules(f)
	cfg := a.store.Get()
	if !cfg.LLM.Enabled || f == nil || len(f.Metrics) == 0 {
		return base
	}
	var out struct {
		Bias    string   `json:"bias"`
		Score   float64  `json:"score"`
		Summary string   `json:"summary"`
		Points  []string `json:"points"`
	}
	meta, _ := json.Marshal(f.Metrics)
	sys := `你是基本面分析师。输出 JSON：{"bias":"bull|bear|neutral","score":-1到1,"summary":"一句话","points":["依据"]}。只输出 JSON。`
	if err := a.llmJSON(ctx, sys, fmt.Sprintf("指标 %s", string(meta)), &out); err != nil {
		return base
	}
	if out.Bias == "" {
		out.Bias = biasOf(out.Score)
	}
	return AgentReport{Role: "基本面分析师", Bias: out.Bias, Score: clamp(out.Score, -1, 1), Summary: out.Summary, Points: out.Points, Source: "llm"}
}

func (a *App) llmTrader(ctx context.Context, d *Decision) (string, float64, float64, string, bool) {
	joined := ""
	for _, r := range d.Reports {
		joined += fmt.Sprintf("[%s] %s（%.2f）\n", r.Role, r.Summary, r.Score)
	}
	sys := `你是交易员。综合报告输出 JSON：{"action":"BUY|SELL|HOLD","target_pct":0到1,"confidence":0到1,"rationale":"中文理由"}。只输出 JSON。`
	var out struct {
		Action     string  `json:"action"`
		TargetPct  float64 `json:"target_pct"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	}
	if err := a.llmJSON(ctx, sys, joined+"\n看多："+d.Bull+"\n看空："+d.Bear, &out); err != nil {
		return "", 0, 0, "", false
	}
	act := strings.ToUpper(strings.TrimSpace(out.Action))
	if act != "BUY" && act != "SELL" && act != "HOLD" {
		act = "HOLD"
	}
	return act, clamp(out.TargetPct, 0, 1), clamp(out.Confidence, 0, 1), out.Rationale, true
}

// ---------------------------------------------------------------- 信号与评分

// markDecision 更新已存决策的执行结果。
func (a *App) markDecision(id string, order *Order, execErr error) {
	a.mu.Lock()
	for i := range a.decisions {
		if a.decisions[i].ID != id {
			continue
		}
		if execErr != nil {
			a.decisions[i].ExecNote = "未执行：" + execErr.Error()
		} else if order != nil {
			a.decisions[i].Executed = true
			a.decisions[i].OrderID = order.ID
			a.decisions[i].ExecNote = fmt.Sprintf("%s %.6f @ %.4f", order.Side, order.Qty, order.Price)
		}
		break
	}
	a.mu.Unlock()
	a.persist()
}

func (a *App) copySignal(id string) (*Order, error) {
	cfg := a.store.Get()
	if cfg.KillSwitch {
		return nil, fmt.Errorf("停机开关已开启")
	}
	a.mu.RLock()
	var d *Decision
	for i := range a.decisions {
		if a.decisions[i].ID == id {
			cp := a.decisions[i]
			d = &cp
			break
		}
	}
	a.mu.RUnlock()
	if d == nil {
		return nil, fmt.Errorf("信号不存在")
	}
	if d.Action == "HOLD" {
		return nil, fmt.Errorf("HOLD 信号无需跟单")
	}
	sym, err := ParseSymbol(d.Symbol)
	if err != nil {
		return nil, err
	}
	bars, err := a.market.Klines(context.Background(), sym, d.Interval, 5)
	if err != nil || len(bars) == 0 {
		return nil, fmt.Errorf("获取最新价失败")
	}
	price := bars[len(bars)-1].Close
	cp := *d
	cp.Price = price
	if cp.Action == "SELL" {
		cp.Target = 0
	}
	if err := a.checkTradeRisk(cp); err != nil {
		return nil, err
	}
	order, err := a.executeDecision(&cp, "跟单")
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	for i := range a.decisions {
		if a.decisions[i].ID == id {
			a.decisions[i].Copied = true
			break
		}
	}
	a.mu.Unlock()
	a.persist()
	return order, nil
}

func (a *App) scoreAll(ctx context.Context) (int, ScoreStats) {
	a.mu.RLock()
	list := append([]Decision{}, a.decisions...)
	a.mu.RUnlock()
	dur := map[string]time.Duration{"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour}
	horizon := 3
	scored := 0
	for _, d := range list {
		if d.Score != nil {
			continue
		}
		bd := dur[d.Interval]
		if bd <= 0 || time.Since(time.Unix(d.CreatedAt, 0)) < time.Duration(horizon)*bd {
			continue
		}
		sym, err := ParseSymbol(d.Symbol)
		if err != nil {
			continue
		}
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		bars, err := a.market.Klines(fctx, sym, d.Interval, 1000)
		cancel()
		if err != nil {
			continue
		}
		idx := -1
		for i := range bars {
			if bars[i].T.Unix() >= d.CreatedAt {
				idx = i
				break
			}
		}
		if idx < 0 || idx+horizon >= len(bars) {
			continue
		}
		entry, exit := bars[idx].Close, bars[idx+horizon].Close
		if entry <= 0 {
			continue
		}
		ret := (exit/entry - 1) * 100
		sc := &DecisionScore{ScoredAt: time.Now().Unix(), HorizonBars: horizon, EntryPrice: entry, ExitPrice: exit, ForwardReturnPct: ret}
		switch d.Action {
		case "BUY":
			sc.Correct = ret > 0
			sc.Note = fmt.Sprintf("买入后 %d 根涨跌 %.2f%%", horizon, ret)
		case "SELL":
			sc.Correct = ret < 0
			sc.Note = fmt.Sprintf("卖出后 %d 根涨跌 %.2f%%", horizon, ret)
		default:
			sc.Correct = absFloat(ret) <= 1
			sc.Note = fmt.Sprintf("HOLD 后 %d 根涨跌 %.2f%%", horizon, ret)
		}
		a.mu.Lock()
		for i := range a.decisions {
			if a.decisions[i].ID == d.ID {
				a.decisions[i].Score = sc
				scored++
				break
			}
		}
		a.mu.Unlock()
	}
	if scored > 0 {
		a.persist()
	}
	return scored, a.stats()
}

func (a *App) stats() ScoreStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := ScoreStats{Total: len(a.decisions)}
	for _, d := range a.decisions {
		if d.Score == nil {
			st.Pending++
			continue
		}
		st.Scored++
		st.AvgRet += d.Score.ForwardReturnPct
		if d.Score.Correct {
			st.Correct++
		}
	}
	if st.Scored > 0 {
		st.HitRate = float64(st.Correct) / float64(st.Scored) * 100
		st.AvgRet /= float64(st.Scored)
	}
	return st
}
