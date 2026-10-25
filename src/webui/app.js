(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var INTERVALS = ['1m', '5m', '15m', '30m', '1h', '4h', '1d'];
  var LLM_PRESETS = {
    'deepseek-flash': { url: 'https://api.deepseek.com', model: 'deepseek-flash' },
    'deepseek-v4-pro': { url: 'https://api.deepseek.com', model: 'deepseek-v4-pro' }
  };
  var state = { settings: null, status: null, decisions: [], targets: [] };

  async function api(path, opts) {
    opts = opts || {};
    var init = { method: opts.method || 'GET', headers: {}, cache: 'no-store' };
    if (opts.body !== undefined) { init.headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(opts.body); }
    var res = await fetch('/api/' + path, init);
    var text = await res.text();
    var data = null;
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = { error: text }; }
    if (!res.ok) { throw new Error((data && (data.error || data.message)) || ('HTTP ' + res.status)); }
    return data;
  }
  function money(v, d) { return (v === null || v === undefined || isNaN(v)) ? '—' : Number(v).toLocaleString('zh-CN', { minimumFractionDigits: d === undefined ? 2 : d, maximumFractionDigits: d === undefined ? 2 : d }); }
  function pct(v) { return (v >= 0 ? '+' : '') + Number(v).toFixed(2) + '%'; }
  function cls(v) { return v > 0 ? 'up' : (v < 0 ? 'down' : ''); }
  function ts(s) { if (!s) { return '—'; } var d = new Date(s * 1000); var p = function (n) { return n < 10 ? '0' + n : n; }; return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()); }
  function esc(s) { return String(s === undefined || s === null ? '' : s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
  var tt = null;
  function toast(msg, err) { var el = $('toast'); el.textContent = msg; el.className = 'toast show' + (err ? ' err' : ''); clearTimeout(tt); tt = setTimeout(function () { el.className = 'toast'; }, 3200); }

  function agentCard(r) {
    var bias = r.bias === 'bull' ? '看多' : (r.bias === 'bear' ? '看空' : '中性');
    return '<div class="agent"><h3>' + esc(r.role) +
      '<span class="badge ' + (r.bias === 'bull' ? 'BUY' : (r.bias === 'bear' ? 'SELL' : 'HOLD')) + '" style="font-size:11px;padding:2px 8px">' + bias + ' ' + Number(r.score).toFixed(2) + '</span>' +
      '<span class="hint" style="margin:0">' + (r.source === 'llm' ? 'LLM' : '规则') + '</span></h3>' +
      '<div>' + esc(r.summary) + '</div>' +
      ((r.points && r.points.length) ? '<ul>' + r.points.map(function (p) { return '<li>' + esc(p) + '</li>'; }).join('') + '</ul>' : '') + '</div>';
  }

  function renderDecision(d) {
    var h = '<div class="decision"><div class="decision-head">' +
      '<span class="badge ' + d.action + '">' + d.action + '</span><b>' + esc(d.symbol) + '</b>' +
      '<span class="hint" style="margin:0">' + esc(d.interval) + ' · 价格 ' + money(d.price, 4) + ' · 目标 ' + (d.target * 100).toFixed(1) + '% · ' + ts(d.created_at) +
      (d.news_auto ? ' · 新闻自动抓取 ' + d.news_count + ' 条' : '') + '</span></div>';
    (d.reports || []).forEach(function (r) { h += agentCard(r); });
    h += '<div class="debate"><div class="agent"><h3><span class="badge BUY" style="font-size:11px;padding:2px 8px">看多</span></h3><div>' + esc(d.bull_case) + '</div></div>' +
      '<div class="agent"><h3><span class="badge SELL" style="font-size:11px;padding:2px 8px">看空</span></h3><div>' + esc(d.bear_case) + '</div></div></div>';
    h += '<div class="agent"><h3>交易员 <span class="hint" style="margin:0">' + (d.source === 'llm' ? 'LLM' : '规则') + ' · 置信度 ' + (d.confidence * 100).toFixed(0) + '%</span></h3><div><b>' + esc(d.action) + '</b> · ' + esc(d.rationale) + '</div></div>';
    if (d.executed) { h += '<p class="hint">✅ 自主交易已执行：' + esc(d.exec_note) + '</p>'; }
    else if (d.exec_note) { h += '<p class="hint">未执行：' + esc(d.exec_note) + '</p>'; }
    if (d.score) { h += '<p class="hint">评分：' + pct(d.score.forward_return_pct) + ' · ' + (d.score.correct ? '<span class="up">判断正确</span>' : '<span class="down">判断错误</span>') + '</p>'; }
    return h + '</div>';
  }

  var VENUES = [{ v: 'ashare', t: 'A 股' }, { v: 'binance', t: '币安' }, { v: 'okx', t: 'OKX' }, { v: 'bitget', t: 'Bitget' }];

  function splitSymbol(sym) {
    var i = (sym || '').indexOf(':');
    if (i <= 0) { return { venue: 'binance', code: sym || '' }; }
    return { venue: sym.slice(0, i), code: sym.slice(i + 1) };
  }

  function renderTargets() {
    var box = $('target-rows');
    if (!box) { return; }
    box.innerHTML = state.targets.map(function (t, i) {
      var sp = splitSymbol(t.symbol);
      var opts = VENUES.map(function (v) {
        return '<option value="' + v.v + '"' + (v.v === sp.venue ? ' selected' : '') + '>' + v.t + '</option>';
      }).join('');
      var isAshare = sp.venue === 'ashare';
      var cur = t.interval || (isAshare ? '1d' : '4h');
      if (isAshare && cur === '4h') { cur = '1d'; }
      var ivOpts = INTERVALS.map(function (v) {
        var dis = isAshare && v === '4h' ? ' disabled' : '';
        return '<option value="' + v + '"' + (v === cur ? ' selected' : '') + dis + '>' + v + '</option>';
      }).join('');
      return '<div class="target-row" data-i="' + i + '">' +
        '<select class="t-venue">' + opts + '</select>' +
        '<input class="t-code" value="' + esc(sp.code) + '" placeholder="' + (isAshare ? '6 位代码，如 600519' : '交易对，如 BTCUSDT') + '">' +
        '<select class="t-interval">' + ivOpts + '</select>' +
        '<input class="t-hours" type="number" min="1" max="168" value="' + (t.every_hours || 6) + '" placeholder="间隔小时">' +
        '<button class="btn sm danger t-del">删除</button></div>';
    }).join('') || '<p class="hint">还没有自动标的，点下面的按钮添加。</p>';

    Array.prototype.forEach.call(box.querySelectorAll('.t-del'), function (b) {
      b.addEventListener('click', function () {
        state.targets = readTargets();
        var i = parseInt(b.closest('.target-row').dataset.i, 10);
        state.targets.splice(i, 1);
        renderTargets();
      });
    });
    Array.prototype.forEach.call(box.querySelectorAll('.t-venue'), function (sel) {
      sel.addEventListener('change', function () {
        var row = sel.closest('.target-row');
        var isAshare = sel.value === 'ashare';
        var iv = row.querySelector('.t-interval');
        Array.prototype.forEach.call(iv.options, function (o) { o.disabled = isAshare && o.value === '4h'; });
        if (isAshare && iv.value === '4h') { iv.value = '1d'; }
        row.querySelector('.t-code').placeholder = isAshare ? '6 位代码，如 600519' : '交易对，如 BTCUSDT';
      });
    });
  }

  function readTargets() {
    var out = [];
    Array.prototype.forEach.call(document.querySelectorAll('#target-rows .target-row'), function (row) {
      var venue = row.querySelector('.t-venue').value;
      var code = row.querySelector('.t-code').value.trim();
      if (!code) { return; }
      if (venue !== 'ashare') { code = code.toUpperCase(); }
      out.push({
        symbol: venue + ':' + code,
        interval: row.querySelector('.t-interval').value,
        every_hours: parseInt(row.querySelector('.t-hours').value, 10) || 6
      });
    });
    return out;
  }

  async function analyze() {
    var btn = $('btn-analyze'); btn.disabled = true; btn.textContent = '分析中…';
    $('an-result').innerHTML = '<p class="hint">分析师并行工作中，配置 LLM 时约 30-60 秒…</p>';
    try {
      var venue = $('an-venue').value;
      var code = $('an-code').value.trim();
      if (!code) { toast('请填写代码或交易对', true); btn.disabled = false; btn.textContent = '运行分析'; return; }
      if (venue === 'ashare' && !/^\d{6}$/.test(code)) { toast('A 股代码应为 6 位数字', true); btn.disabled = false; btn.textContent = '运行分析'; return; }
      var symbol = venue + ':' + (venue === 'ashare' ? code : code.toUpperCase());
      var d = await api('analyze', { method: 'POST', body: { symbol: symbol, interval: $('an-interval').value, news: $('an-news').value } });
      $('an-result').innerHTML = renderDecision(d);
      refreshStatus(); refreshSignals(); refreshRecent();
      toast('结论：' + d.action);
    } catch (e) { $('an-result').innerHTML = '<p class="hint down">' + esc(e.message) + '</p>'; toast(e.message, true); }
    finally { btn.disabled = false; btn.textContent = '运行分析'; }
  }

  async function refreshStatus() {
    try {
      var s = await api('status');
      state.status = s;
      var pill = $('status-pill');
      pill.className = 'pill ' + (s.kill_switch ? 'bad' : 'ok');
      pill.querySelector('span').textContent = (s.kill_switch ? '已停机' : '运行中') + ' · ' + (s.auto && s.auto.enabled ? '自主交易' : '手动') + (s.llm && s.llm.enabled ? ' · LLM' : ' · 规则');
      $('btn-kill').textContent = s.kill_switch ? '恢复' : '停机';
      ['crypto', 'ashare'].forEach(function (m) {
        var acc = s.accounts[m]; if (!acc) { return; }
        var pnl = acc.equity - acc.initial_cash;
        $('acc-' + m).innerHTML = '<div class="stat"><div class="stat-label">净值</div><div class="stat-value ' + cls(pnl) + '">' + money(acc.equity) + '</div></div>' +
          '<div class="stat"><div class="stat-label">可用</div><div class="stat-value">' + money(acc.cash) + '</div></div>' +
          '<div class="stat"><div class="stat-label">已实现盈亏</div><div class="stat-value ' + cls(acc.realized_pnl) + '">' + money(acc.realized_pnl) + '</div></div>';
        var rows = (acc.positions || []).map(function (p) {
          return '<tr><td>' + esc(p.symbol) + '</td><td>' + money(p.qty, 6) + '</td><td>' + money(p.avg_cost, 4) + '</td><td>' + money(p.last_price, 4) + '</td><td class="' + cls(p.unrealized_pnl) + '">' + money(p.unrealized_pnl) + '</td></tr>';
        }).join('');
        $('pos-' + m).innerHTML = '<tr><th>标的</th><th>数量</th><th>成本</th><th>现价</th><th>浮动盈亏</th></tr>' + (rows || '<tr><td class="hint" colspan="5">暂无持仓</td></tr>');
      });
    } catch (e) { var p = $('status-pill'); p.className = 'pill bad'; p.querySelector('span').textContent = '连接失败'; }
  }

  async function refreshOrders() {
    try {
      var orders = await api('orders?limit=50');
      $('order-table').innerHTML = '<tr><th>时间</th><th>标的</th><th>方向</th><th>数量</th><th>价格</th><th>金额</th><th>理由</th></tr>' +
        (orders.map(function (o) {
          return '<tr><td>' + ts(o.created_at) + '</td><td>' + esc(o.symbol) + '</td><td class="' + (o.side === 'buy' ? 'up' : 'down') + '">' + esc(o.side) + '</td><td>' + money(o.qty, 6) + '</td><td>' + money(o.price, 4) + '</td><td>' + money(o.notional) + '</td><td class="reason" title="' + esc(o.reason) + '"><span class="clamp">' + esc(o.reason) + '</span></td></tr>';
        }).join('') || '<tr><td class="hint" colspan="7">还没有成交</td></tr>');
    } catch (e) { /* 忽略 */ }
  }

  async function refreshSignals() {
    try {
      var st = await api('stats');
      $('sig-stats').innerHTML = [
        ['信号总数', st.total + '', 0], ['已评分', st.scored + '', 0], ['命中率', (st.hit_rate_pct || 0).toFixed(0) + '%', 0], ['平均后续', pct(st.avg_return_pct || 0), st.avg_return_pct], ['待评分', st.pending + '', 0]
      ].map(function (kv) { return '<div class="stat"><div class="stat-label">' + kv[0] + '</div><div class="stat-value ' + cls(kv[2]) + '">' + kv[1] + '</div></div>'; }).join('');
      var list = await api('decisions?limit=20');
      state.decisions = list;
      $('sig-list').innerHTML = list.length ? list.map(function (d) {
        return '<div class="decision"><div class="decision-head"><span class="badge ' + d.action + '">' + d.action + '</span><b>' + esc(d.symbol) + '</b>' +
          '<span class="hint" style="margin:0">' + esc(d.interval) + ' · ' + ts(d.created_at) + ' · 置信度 ' + (d.confidence * 100).toFixed(0) + '%' +
          (d.score ? ' · 评分 ' + pct(d.score.forward_return_pct) + (d.score.correct ? ' ✓' : ' ✗') : ' · 待评分') + '</span>' +
          '<span style="margin-left:auto">' + (d.action !== 'HOLD' ? '<button class="btn copy" data-id="' + esc(d.id) + '">跟单</button>' : '') + '</span></div>' +
          '<div class="hint" style="margin:0">' + esc(d.rationale) + (d.executed ? ' · 已自主执行' : '') + (d.copied ? ' · 已跟单' : '') + '</div></div>';
      }).join('') : '<p class="hint">还没有信号，先去「分析」页跑一次。</p>';
      Array.prototype.forEach.call(document.querySelectorAll('.copy'), function (b) {
        b.addEventListener('click', async function () {
          try { await api('copy', { method: 'POST', body: { id: b.dataset.id } }); toast('已跟单'); refreshStatus(); refreshOrders(); refreshSignals(); }
          catch (e) { toast(e.message, true); }
        });
      });
    } catch (e) { /* 忽略 */ }
  }

  async function runScore() {
    $('btn-score').disabled = true;
    try { var r = await api('score', { method: 'POST' }); toast('评分完成：新增 ' + r.scored + ' 条'); refreshSignals(); }
    catch (e) { toast(e.message, true); }
    finally { $('btn-score').disabled = false; }
  }

  function fillSettings(s) {
    state.settings = s;
    $('st-proxy').value = s.proxy_url || '';
    if ($('st-market-proxy')) { $('st-market-proxy').checked = s.market_proxy !== false; }
    if ($('st-symbol')) { $('st-symbol').value = s.default_symbol || ''; }
    $('st-auto').checked = !!s.auto.enabled;
    if ($('st-auto-interval')) { $('st-auto-interval').value = s.auto.interval || '4h'; }
    $('st-news-auto').checked = !!(s.news && s.news.auto);
    $('st-manual-trade').checked = !!s.auto.manual_trade;
    state.targets = (s.auto.targets || []).map(function (t) { return { symbol: t.symbol, interval: t.interval || '', every_hours: t.every_hours || 0 }; });
    if (!state.targets.length) {
      state.targets = (s.auto.watchlist || []).map(function (sym) { return { symbol: sym, interval: '', every_hours: 0 }; });
    }
    renderTargets();
    $('st-news-max').value = (s.news && s.news.max_items) || 8;
    if ($('st-every')) { $('st-every').value = s.auto.every_hours; }
    $('st-conf').value = Math.round(s.auto.min_confidence * 100);
    $('st-pos').value = Math.round(s.risk.max_position_pct * 100);
    $('st-max').value = s.risk.max_open_trades;
    $('st-min').value = s.risk.min_notional;
    $('st-llm').checked = !!s.llm.enabled;
    $('st-llm-url').value = s.llm.base_url || '';
    $('st-llm-model').value = s.llm.model || '';
    $('st-llm-key').value = '';
    $('st-llm-proxy').checked = !!s.llm.use_proxy;
    $('llm-status').textContent = s.llm.enabled ? ('当前：LLM · ' + (s.llm.model || '')) : '当前：规则模式';
    var matched = '';
    Object.keys(LLM_PRESETS).forEach(function (k) {
      if (!matched && LLM_PRESETS[k].model === s.llm.model && LLM_PRESETS[k].url === s.llm.base_url) { matched = k; }
    });
    $('st-llm-preset').value = matched || 'deepseek-flash';
  }

  async function saveSettings() {
    try {
      var s = JSON.parse(JSON.stringify(state.settings));
      s.proxy_url = $('st-proxy').value.trim();
      s.market_proxy = $('st-market-proxy') ? $('st-market-proxy').checked : true;
      if ($('st-symbol')) { s.default_symbol = $('st-symbol').value.trim(); }
      s.auto.enabled = $('st-auto').checked;
      if ($('st-every')) { s.auto.every_hours = parseInt($('st-every').value, 10) || 6; }
      if ($('st-auto-interval')) { s.auto.interval = $('st-auto-interval').value; }
      s.news = s.news || {};
      s.auto.manual_trade = $('st-manual-trade').checked;
      s.auto.targets = readTargets();
      s.auto.watchlist = s.auto.targets.map(function (t) { return t.symbol; });
      s.news.auto = $('st-news-auto').checked;
      s.news.max_items = parseInt($('st-news-max').value, 10) || 8;
      s.auto.min_confidence = (parseFloat($('st-conf').value) || 55) / 100;
      s.risk.max_position_pct = (parseFloat($('st-pos').value) || 25) / 100;
      s.risk.max_open_trades = parseInt($('st-max').value, 10) || 3;
      s.risk.min_notional = parseFloat($('st-min').value) || 10;
      s.llm.enabled = $('st-llm').checked;
      s.llm.base_url = $('st-llm-url').value.trim();
      s.llm.model = $('st-llm-model').value.trim();
      s.llm.use_proxy = $('st-llm-proxy').checked;
      if ($('st-llm-key').value.trim()) { s.llm.api_key = $('st-llm-key').value.trim(); }
      var out = await api('settings', { method: 'PUT', body: s });
      fillSettings(out);
      toast('设置已保存');
      refreshStatus();
    } catch (e) { toast(e.message, true); }
  }

  async function toggleKill() {
    var on = !(state.status && state.status.kill_switch);
    if (on && !confirm('停机后不再自主交易，确定？')) { return; }
    try { await api('kill', { method: 'POST', body: { on: on } }); toast(on ? '已停机' : '已恢复'); refreshStatus(); }
    catch (e) { toast(e.message, true); }
  }

  async function refreshLogs() {
    try { var d = await api('logs?lines=200'); var el = $('logs'); el.textContent = (d.lines || []).join('\n') || '（暂无日志）'; el.scrollTop = el.scrollHeight; }
    catch (e) { /* 忽略 */ }
  }

  var recent = [];
  function renderCodeList() {
    var venue = $('an-venue').value;
    var seen = {};
    var opts = [];
    recent.forEach(function (r) {
      if (r.venue !== venue || seen[r.code]) { return; }
      seen[r.code] = true;
      opts.push('<option value="' + esc(r.code) + '" label="' + esc(r.interval) + '"></option>');
    });
    $('an-code-list').innerHTML = opts.join('');
  }

  async function refreshRecent() {
    try {
      var list = await api('decisions?limit=50');
      var seen = {};
      recent = [];
      (list || []).forEach(function (d) {
        if (!d.symbol) { return; }
        var i = d.symbol.indexOf(':');
        if (i <= 0) { return; }
        var venue = d.symbol.slice(0, i);
        var code = d.symbol.slice(i + 1);
        var key = venue + '|' + code;
        if (seen[key]) { return; }
        seen[key] = true;
        recent.push({ venue: venue, code: code, interval: d.interval });
      });
      renderCodeList();
    } catch (e) { /* 忽略 */ }
  }

  function syncVenue() {
    var venue = $('an-venue').value;
    var iv = $('an-interval');
    var isAshare = venue === 'ashare';
    Array.prototype.forEach.call(iv.options, function (o) { o.disabled = isAshare && o.value === '4h'; });
    if (isAshare && iv.value === '4h') { iv.value = '1d'; }
    $('an-code').placeholder = isAshare ? '6 位代码，如 600519' : '交易对，如 BTCUSDT / BTC-USDT';
    renderCodeList();
  }

  function switchTab(name) {
    Array.prototype.forEach.call(document.querySelectorAll('.tab'), function (b) { b.classList.toggle('active', b.dataset.tab === name); });
    Array.prototype.forEach.call(document.querySelectorAll('.pane'), function (p) { p.classList.toggle('active', p.id === 'pane-' + name); });
    if (name === 'signals') { refreshSignals(); }
    if (name === 'portfolio') { refreshStatus(); refreshOrders(); }
    if (name === 'logs') { refreshLogs(); }
  }

  async function init() {
    try {
    $('an-interval').innerHTML = INTERVALS.map(function (v) { return '<option>' + v + '</option>'; }).join('');
    $('an-interval').value = '4h';
    syncVenue();
    Array.prototype.forEach.call(document.querySelectorAll('.tab'), function (b) { b.addEventListener('click', function () { switchTab(b.dataset.tab); }); });
    $('btn-analyze').addEventListener('click', analyze);
    $('btn-score').addEventListener('click', runScore);
    $('btn-save').addEventListener('click', saveSettings);
    $('btn-kill').addEventListener('click', toggleKill);
    $('btn-add-target').addEventListener('click', function () {
      state.targets = readTargets();
      state.targets.push({ symbol: '', interval: '', every_hours: 0 });
      renderTargets();
    });
    $('an-venue').addEventListener('change', syncVenue);
    $('an-code').addEventListener('input', function () {
      var v = $('an-code').value.trim();
      var opt = Array.prototype.find.call($('an-code-list').options, function (o) { return o.value === v; });
      if (opt && opt.label) {
        var iv = $('an-interval');
        var blocked = Array.prototype.some.call(iv.options, function (o) { return o.disabled && o.value === opt.label; });
        if (!blocked) { iv.value = opt.label; }
      }
    });
    $('st-llm-preset').addEventListener('change', function () {
      var p = LLM_PRESETS[this.value];
      if (!p) { return; }
      $('st-llm-url').value = p.url;
      $('st-llm-model').value = p.model;
      $('st-llm').checked = true;
      $('st-llm-proxy').checked = !!p.proxy;
    });
    $('btn-llm-test').addEventListener('click', async function () {
      var btn = this;
      btn.disabled = true; btn.textContent = '测试中…';
      try {
        var r = await api('llm/test', { method: 'POST', body: {
          base_url: $('st-llm-url').value.trim(),
          model: $('st-llm-model').value.trim(),
          api_key: $('st-llm-key').value.trim(),
          use_proxy: $('st-llm-proxy').checked
        }});
        toast('连接成功：' + r.model + '，' + r.latency_ms + 'ms');
      } catch (e) { toast(e.message, true); }
      finally { btn.disabled = false; btn.textContent = '测试连接'; }
    });
    refreshRecent();
    } catch (e) { console.error('初始化部分失败', e); }
    try { fillSettings(await api('settings')); } catch (e) { console.error('设置加载失败', e); }
    await refreshStatus();
    setInterval(refreshStatus, 10000);
  }

  if (document.readyState === 'loading') { document.addEventListener('DOMContentLoaded', init); } else { init(); }
})();
