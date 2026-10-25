package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed webui/index.html webui/app.css webui/app.js
var uiFS embed.FS

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" && !isLocalRequest(r) {
		http.Error(w, "该界面仅允许在局域网内访问", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/healthz":
		io.WriteString(w, "ok")
	case r.URL.Path == "/":
		serveUI(w, "index.html", "text/html; charset=utf-8")
	case r.URL.Path == "/app.css":
		serveUI(w, "app.css", "text/css; charset=utf-8")
	case r.URL.Path == "/app.js":
		serveUI(w, "app.js", "application/javascript; charset=utf-8")
	case strings.HasPrefix(r.URL.Path, "/api/"):
		a.handleAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func serveUI(w http.ResponseWriter, name, ctype string) {
	data, err := uiFS.ReadFile("webui/" + name)
	if err != nil {
		http.Error(w, "资源缺失", http.StatusInternalServerError)
		return
	}
	if name == "index.html" {
		data = bytes.ReplaceAll(data, []byte("{{V}}"), []byte(appVersion))
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func isLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch path {
	case "status":
		a.apiStatus(w, r)
	case "settings":
		a.apiSettings(w, r)
	case "analyze":
		a.apiAnalyze(w, r)
	case "decisions":
		a.apiDecisions(w, r)
	case "score":
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		n, st := a.scoreAll(ctx)
		writeJSON(w, http.StatusOK, map[string]any{"scored": n, "stats": st})
	case "stats":
		writeJSON(w, http.StatusOK, a.stats())
	case "copy":
		a.apiCopy(w, r)
	case "orders":
		writeJSON(w, http.StatusOK, a.broker.Orders(intParam(r, "limit", 50)))
	case "kill":
		a.apiKill(w, r)
	case "auto/run":
		if r.Method != http.MethodPost {
			apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
		defer cancel()
		out := a.runAutoCycle(ctx, true)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "decisions": out})
	case "llm/test":
		a.apiLLMTest(w, r)
	case "logs":
		writeJSON(w, http.StatusOK, map[string]any{"lines": a.logs.Tail(intParam(r, "lines", 200))})
	default:
		apiErr(w, http.StatusNotFound, "未知接口")
	}
}

func (a *App) apiStatus(w http.ResponseWriter, r *http.Request) {
	s := a.store.Get()
	accounts := map[string]any{}
	for _, m := range []string{"crypto", "ashare"} {
		acc := a.broker.Account(m)
		if acc == nil {
			continue
		}
		accounts[m] = map[string]any{
			"cash": acc.Cash, "equity": a.broker.Equity(m), "initial_cash": acc.InitialCash,
			"realized_pnl": acc.RealizedPnL, "positions": a.broker.PositionsView(m),
		}
	}
	a.mu.RLock()
	var last *Decision
	if len(a.decisions) > 0 {
		d := a.decisions[0]
		last = &d
	}
	a.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"version": appVersion, "mode": "paper", "kill_switch": s.KillSwitch,
		"auto": s.Auto, "llm": map[string]any{"enabled": s.LLM.Enabled, "model": s.LLM.Model},
		"accounts": accounts, "stats": a.stats(), "last_decision": last,
	})
}

func (a *App) apiSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s := a.store.Get()
		if s.LLM.APIKey != "" {
			s.LLM.APIKey = "***"
		}
		writeJSON(w, http.StatusOK, s)
	case http.MethodPut, http.MethodPost:
		var in Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
			apiErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
			return
		}
		cur := a.store.Get()
		if in.LLM.APIKey == "" || in.LLM.APIKey == "***" {
			in.LLM.APIKey = cur.LLM.APIKey
		}
		in.KillSwitch = cur.KillSwitch
		out, err := a.store.Update(func(s *Settings) error { *s = in; return nil })
		if err != nil {
			apiErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.market.SetProxy(marketProxy(out))
		if out.LLM.APIKey != "" {
			out.LLM.APIKey = "***"
		}
		writeJSON(w, http.StatusOK, out)
	default:
		apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
	}
}

func (a *App) apiAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var req AgentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		apiErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(req.Symbol) == "" {
		cfg := a.store.Get()
		if len(cfg.Auto.Targets) > 0 {
			req.Symbol = cfg.Auto.Targets[0].Symbol
		} else {
			req.Symbol = cfg.DefaultSymbol
		}
	}
	if strings.TrimSpace(req.Symbol) == "" {
		apiErr(w, http.StatusBadRequest, "请填写标的")
		return
	}
	d, err := a.runAgent(r.Context(), req, modeManual)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (a *App) apiDecisions(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 20)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if limit > len(a.decisions) {
		limit = len(a.decisions)
	}
	writeJSON(w, http.StatusOK, a.decisions[:limit])
}

func (a *App) apiCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	order, err := a.copySignal(in.ID)
	if err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "order": order})
}

func (a *App) apiKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	if _, err := a.store.Update(func(s *Settings) error { s.KillSwitch = in.On; return nil }); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.On {
		a.logf("已开启停机开关")
	} else {
		a.logf("已关闭停机开关")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kill_switch": in.On})
}

func (a *App) apiLLMTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiErr(w, http.StatusMethodNotAllowed, "方法不支持")
		return
	}
	var req struct {
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
		UseProxy bool   `json:"use_proxy"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req)
	s := a.store.Get().LLM
	if strings.TrimSpace(req.BaseURL) != "" {
		s.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if strings.TrimSpace(req.Model) != "" {
		s.Model = strings.TrimSpace(req.Model)
	}
	if strings.TrimSpace(req.APIKey) != "" {
		s.APIKey = strings.TrimSpace(req.APIKey)
	}
	s.UseProxy = req.UseProxy
	s.Enabled = true
	if !strings.HasPrefix(s.BaseURL, "http://") && !strings.HasPrefix(s.BaseURL, "https://") {
		apiErr(w, http.StatusBadRequest, "接口地址需以 http:// 或 https:// 开头")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var out struct {
		OK bool `json:"ok"`
	}
	start := time.Now()
	if err := a.llmJSONWith(ctx, s, `只输出 JSON：{"ok":true}`, "ping", &out); err != nil {
		apiErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "latency_ms": time.Since(start).Milliseconds(), "model": s.Model, "reply": out.OK,
	})
}

func intParam(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
