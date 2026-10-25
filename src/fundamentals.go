package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type Fundamentals struct {
	Symbol  string             `json:"symbol"`
	Market  string             `json:"market"`
	Source  string             `json:"source"`
	Metrics map[string]float64 `json:"metrics"`
	Notes   []string           `json:"notes"`
}

var tencentQuoteRe = regexp.MustCompile(`="(.*)"`)

// fundamentals 目前只对 A 股取估值数据（腾讯行情），加密资产返回无数据。
func (a *App) fundamentals(ctx context.Context, sym Symbol) (*Fundamentals, error) {
	if sym.Venue != "ashare" {
		return nil, fmt.Errorf("加密资产没有财报数据")
	}
	prefix := "sh"
	if strings.HasPrefix(sym.Code, "0") || strings.HasPrefix(sym.Code, "3") {
		prefix = "sz"
	} else if strings.HasPrefix(sym.Code, "4") || strings.HasPrefix(sym.Code, "8") {
		prefix = "bj"
	}
	u := "https://qt.gtimg.cn/q=" + prefix + sym.Code
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; aitrader/1.0)")
	resp, err := a.market.direct.Do(req)
	if err != nil {
		return nil, fmt.Errorf("腾讯行情请求失败：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	m := tencentQuoteRe.FindSubmatch(body)
	if len(m) < 2 {
		return nil, fmt.Errorf("腾讯行情返回格式异常")
	}
	fields := strings.Split(string(m[1]), "~")
	at := func(i int) float64 {
		if i >= len(fields) {
			return 0
		}
		v, _ := strconv.ParseFloat(strings.TrimSpace(fields[i]), 64)
		return v
	}
	f := &Fundamentals{Symbol: sym.String(), Market: "ashare", Source: "腾讯行情", Metrics: map[string]float64{}}
	f.Metrics["price"] = at(3)
	f.Metrics["turnover_pct"] = at(38)
	f.Metrics["pe_ttm"] = at(39)
	f.Metrics["total_mcap_yi"] = at(45)
	f.Metrics["pb"] = at(46)
	if f.Metrics["pe_ttm"] <= 0 {
		f.Notes = append(f.Notes, "市盈率为负或缺失（可能亏损）")
	}
	return f, nil
}
