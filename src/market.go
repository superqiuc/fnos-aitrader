package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Bar struct {
	T                              time.Time
	Open, High, Low, Close, Volume float64
}

type Symbol struct {
	Venue string
	Code  string
}

func (s Symbol) String() string { return s.Venue + ":" + s.Code }

func (s Symbol) Market() string {
	if s.Venue == "ashare" {
		return "ashare"
	}
	return "crypto"
}

var supportedIntervals = map[string]bool{"1m": true, "5m": true, "15m": true, "30m": true, "1h": true, "4h": true, "1d": true}

func ParseSymbol(raw string) (Symbol, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Symbol{}, errors.New("标的不能为空")
	}
	if i := strings.Index(raw, ":"); i > 0 {
		venue := strings.ToLower(strings.TrimSpace(raw[:i]))
		code := strings.TrimSpace(raw[i+1:])
		switch venue {
		case "binance", "okx", "bitget":
			if code == "" {
				return Symbol{}, fmt.Errorf("标的代码为空：%s", raw)
			}
			return Symbol{Venue: venue, Code: strings.ToUpper(code)}, nil
		case "ashare":
			if !isAShareCode(code) {
				return Symbol{}, fmt.Errorf("A 股代码应为 6 位数字：%s", code)
			}
			return Symbol{Venue: "ashare", Code: code}, nil
		}
		return Symbol{}, fmt.Errorf("未知市场：%s（可选 binance / okx / bitget / ashare）", venue)
	}
	if isAShareCode(raw) {
		return Symbol{Venue: "ashare", Code: raw}, nil
	}
	return Symbol{}, fmt.Errorf("无法识别的标的：%s", raw)
}

func isAShareCode(c string) bool {
	if len(c) != 6 {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atof(v string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64); return f }

type Market struct {
	mu     sync.RWMutex
	proxy  string
	client *http.Client
	direct *http.Client
}

func NewMarket(proxy string) *Market {
	m := &Market{direct: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	m.SetProxy(proxy)
	return m
}

func (m *Market) SetProxy(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proxy = p
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}
	if strings.TrimSpace(p) != "" {
		if u, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	m.client = &http.Client{Timeout: 20 * time.Second, Transport: tr}
}

func (m *Market) httpClient() *http.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client
}

func (m *Market) getJSON(ctx context.Context, c *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("请求失败：%v（海外交易所请在设置里配置代理）", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		if len(body) > 200 {
			body = body[:200]
		}
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func (m *Market) Klines(ctx context.Context, sym Symbol, interval string, limit int) ([]Bar, error) {
	if !supportedIntervals[interval] {
		return nil, fmt.Errorf("不支持的周期：%s", interval)
	}
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	switch sym.Venue {
	case "binance":
		return m.binance(ctx, sym.Code, interval, limit)
	case "okx":
		return m.okx(ctx, sym.Code, interval, limit)
	case "bitget":
		return m.bitget(ctx, sym.Code, interval, limit)
	case "ashare":
		switch interval {
		case "1m", "5m", "15m", "30m", "1h", "1d":
		default:
			return nil, fmt.Errorf("A 股暂不支持 %s 周期（可用 1m/5m/15m/30m/1h/1d）", interval)
		}
		return m.ashare(ctx, sym.Code, interval, limit)
	}
	return nil, fmt.Errorf("不支持的行情源：%s", sym.Venue)
}

func (m *Market) binance(ctx context.Context, code, interval string, limit int) ([]Bar, error) {
	u := fmt.Sprintf("https://api.binance.com/api/v3/klines?symbol=%s&interval=%s&limit=%d", code, interval, limit)
	var raw [][]any
	if err := m.getJSON(ctx, m.httpClient(), u, &raw); err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(raw))
	for _, r := range raw {
		if len(r) < 6 {
			continue
		}
		out = append(out, Bar{
			T: time.UnixMilli(int64(atof(fmt.Sprint(r[0])))), Open: atof(fmt.Sprint(r[1])),
			High: atof(fmt.Sprint(r[2])), Low: atof(fmt.Sprint(r[3])), Close: atof(fmt.Sprint(r[4])), Volume: atof(fmt.Sprint(r[5])),
		})
	}
	return out, nil
}

func (m *Market) okx(ctx context.Context, code, interval string, limit int) ([]Bar, error) {
	bar := map[string]string{"1m": "1m", "5m": "5m", "15m": "15m", "30m": "30m", "1h": "1H", "4h": "4H", "1d": "1D"}[interval]
	u := fmt.Sprintf("https://www.okx.com/api/v5/market/candles?instId=%s&bar=%s&limit=%d", code, bar, minInt(300, limit))
	var resp struct {
		Code string     `json:"code"`
		Data [][]string `json:"data"`
	}
	if err := m.getJSON(ctx, m.httpClient(), u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("OKX 返回 %s", resp.Code)
	}
	out := make([]Bar, 0, len(resp.Data))
	for _, r := range resp.Data {
		if len(r) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(r[0], 10, 64)
		out = append(out, Bar{T: time.UnixMilli(ts), Open: atof(r[1]), High: atof(r[2]), Low: atof(r[3]), Close: atof(r[4]), Volume: atof(r[5])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out, nil
}

func (m *Market) bitget(ctx context.Context, code, interval string, limit int) ([]Bar, error) {
	u := fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/candles?symbol=%s&granularity=%s&limit=%d", code, interval, limit)
	var resp struct {
		Code string     `json:"code"`
		Data [][]string `json:"data"`
	}
	if err := m.getJSON(ctx, m.httpClient(), u, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "00000" {
		return nil, fmt.Errorf("Bitget 返回 %s", resp.Code)
	}
	out := make([]Bar, 0, len(resp.Data))
	for _, r := range resp.Data {
		if len(r) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(r[0], 10, 64)
		out = append(out, Bar{T: time.UnixMilli(ts), Open: atof(r[1]), High: atof(r[2]), Low: atof(r[3]), Close: atof(r[4]), Volume: atof(r[5])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out, nil
}

func (m *Market) ashare(ctx context.Context, code, interval string, limit int) ([]Bar, error) {
	prefix := "sh"
	if strings.HasPrefix(code, "0") || strings.HasPrefix(code, "3") {
		prefix = "sz"
	}
	bars, err1 := m.ashareEastmoney(ctx, prefix, code, interval, limit)
	if err1 == nil && len(bars) > 0 {
		return bars, nil
	}
	bars2, err2 := m.ashareSina(ctx, prefix, code, interval, limit)
	if err2 == nil && len(bars2) > 0 {
		return bars2, nil
	}
	return nil, fmt.Errorf("A 股行情获取失败：东方财富 %v；新浪 %v", err1, err2)
}

// ashareEastmoney 支持分钟线与日线。
func (m *Market) ashareEastmoney(ctx context.Context, prefix, code, interval string, limit int) ([]Bar, error) {
	klt := map[string]int{"1m": 1, "5m": 5, "15m": 15, "30m": 30, "1h": 60, "1d": 101}[interval]
	if klt == 0 {
		return nil, fmt.Errorf("不支持的周期 %s", interval)
	}
	secid := "1." + code
	if prefix == "sz" {
		secid = "0." + code
	}
	u := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/kline/get?secid=%s&fields1=f1,f2,f3,f4,f5,f6&fields2=f51,f52,f53,f54,f55,f56&klt=%d&fqt=1&lmt=%d&end=20500101", secid, klt, maxInt(limit, 320))
	var resp struct {
		Data struct {
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := m.getJSON(ctx, m.direct, u, &resp); err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(resp.Data.Klines))
	for _, line := range resp.Data.Klines {
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", f[0], time.Local)
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02", f[0], time.Local)
			if err != nil {
				continue
			}
		}
		out = append(out, Bar{T: t, Open: atof(f[1]), Close: atof(f[2]), High: atof(f[3]), Low: atof(f[4])})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("K 线为空")
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// ashareSina 备用源，日线 scale=240。
func (m *Market) ashareSina(ctx context.Context, prefix, code, interval string, limit int) ([]Bar, error) {
	scale := map[string]int{"5m": 5, "15m": 15, "30m": 30, "1h": 60, "1d": 240}[interval]
	if scale == 0 {
		return nil, fmt.Errorf("新浪源不支持 %s", interval)
	}
	u := fmt.Sprintf("https://quotes.sina.cn/cn/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s%s&scale=%d&ma=no&datalen=%d", prefix, code, scale, maxInt(limit, 320))
	var rows []struct {
		Day   string `json:"day"`
		Open  string `json:"open"`
		High  string `json:"high"`
		Low   string `json:"low"`
		Close string `json:"close"`
	}
	if err := m.getJSON(ctx, m.direct, u, &rows); err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(rows))
	for _, r := range rows {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", r.Day, time.Local)
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02", r.Day, time.Local)
		}
		out = append(out, Bar{T: t, Open: atof(r.Open), Close: atof(r.Close), High: atof(r.High), Low: atof(r.Low)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("K 线为空")
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Quote 取最新成交价（比 K 线更轻量，用于持仓市值刷新）。
func (m *Market) Quote(ctx context.Context, sym Symbol) (float64, error) {
	switch sym.Venue {
	case "binance":
		var resp struct {
			Price string `json:"price"`
		}
		u := "https://api.binance.com/api/v3/ticker/price?symbol=" + sym.Code
		if err := m.getJSON(ctx, m.httpClient(), u, &resp); err != nil {
			return 0, err
		}
		if v := atof(resp.Price); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("币安未返回报价")
	case "okx":
		var resp struct {
			Code string `json:"code"`
			Data []struct {
				Last string `json:"last"`
			} `json:"data"`
		}
		u := "https://www.okx.com/api/v5/market/ticker?instId=" + sym.Code
		if err := m.getJSON(ctx, m.httpClient(), u, &resp); err != nil {
			return 0, err
		}
		if len(resp.Data) == 0 {
			return 0, fmt.Errorf("OKX 未返回报价")
		}
		return atof(resp.Data[0].Last), nil
	case "bitget":
		var resp struct {
			Code string `json:"code"`
			Data []struct {
				LastPr string `json:"lastPr"`
			} `json:"data"`
		}
		u := "https://api.bitget.com/api/v2/spot/market/tickers?symbol=" + sym.Code
		if err := m.getJSON(ctx, m.httpClient(), u, &resp); err != nil {
			return 0, err
		}
		if len(resp.Data) == 0 {
			return 0, fmt.Errorf("Bitget 未返回报价")
		}
		return atof(resp.Data[0].LastPr), nil
	case "ashare":
		prefix := "sh"
		if strings.HasPrefix(sym.Code, "0") || strings.HasPrefix(sym.Code, "3") {
			prefix = "sz"
		}
		var body struct {
			Data map[string]string `json:"data"`
		}
		_ = body
		u := "https://qt.gtimg.cn/q=" + prefix + sym.Code
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		resp, err := m.direct.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		match := tencentQuoteRe.FindSubmatch(raw)
		if len(match) < 2 {
			return 0, fmt.Errorf("A 股报价解析失败")
		}
		fields := strings.Split(string(match[1]), "~")
		if len(fields) < 4 {
			return 0, fmt.Errorf("A 股报价字段不足")
		}
		if v := atof(fields[3]); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("A 股报价为空")
	}
	return 0, fmt.Errorf("不支持的标的：%s", sym.String())
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
