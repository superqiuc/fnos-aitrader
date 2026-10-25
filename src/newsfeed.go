package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type eastmoneyNewsResp struct {
	Result struct {
		Items []struct {
			Date    string `json:"date"`
			Title   string `json:"title"`
			Content string `json:"content"`
			Media   string `json:"mediaName"`
		} `json:"cmsArticleWebOld"`
	} `json:"result"`
}

// autoNews 用东方财富搜索接口按标的抓取相关新闻。
// A 股用代码，加密用币种（BTCUSDT -> BTC）。
func (a *App) autoNews(ctx context.Context, sym Symbol, maxItems int) (string, int, error) {
	if maxItems <= 0 || maxItems > 20 {
		maxItems = 8
	}
	keyword := sym.Code
	if sym.Market() == "crypto" {
		base := sym.Code
		if i := strings.Index(base, "-"); i > 0 {
			base = base[:i]
		}
		base = strings.TrimSuffix(base, "USDT")
		keyword = base
	}
	param := fmt.Sprintf(`{"uid":"","keyword":%q,"type":["cmsArticleWebOld"],"client":"web","clientType":"web","clientVersion":"curr","param":{"cmsArticleWebOld":{"searchScope":"default","sort":"default","pageIndex":1,"pageSize":%d,"preTag":"","postTag":""}}}`, keyword, maxItems)
	u := "https://search-api-web.eastmoney.com/search/jsonp?cb=cb&param=" + url.QueryEscape(param)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; aitrader/1.0)")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("新闻抓取失败：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	text := strings.TrimSpace(string(body))
	if i := strings.Index(text, "("); i >= 0 {
		text = text[i+1:]
	}
	if j := strings.LastIndex(text, ")"); j >= 0 {
		text = text[:j]
	}
	var parsed eastmoneyNewsResp
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return "", 0, fmt.Errorf("新闻解析失败：%v", err)
	}
	var sb strings.Builder
	n := 0
	for _, it := range parsed.Result.Items {
		title := strings.TrimSpace(it.Title)
		if title == "" {
			continue
		}
		summary := strings.TrimSpace(it.Content)
		if len([]rune(summary)) > 120 {
			summary = string([]rune(summary)[:120]) + "…"
		}
		line := fmt.Sprintf("【%s】%s（%s）", title, summary, it.Date)
		if sb.Len()+len(line) > 3000 {
			break
		}
		sb.WriteString(line + "\n")
		n++
	}
	if n == 0 {
		return "", 0, fmt.Errorf("没有抓到相关新闻")
	}
	return sb.String(), n, nil
}
