package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type Position struct {
	Symbol   string  `json:"symbol"`
	Qty      float64 `json:"qty"`
	AvgCost  float64 `json:"avg_cost"`
	Last     float64 `json:"last_price"`
	OpenedAt int64   `json:"opened_at"`
}

type Account struct {
	Market      string               `json:"market"`
	Cash        float64              `json:"cash"`
	InitialCash float64              `json:"initial_cash"`
	RealizedPnL float64              `json:"realized_pnl"`
	FeesPaid    float64              `json:"fees_paid"`
	Positions   map[string]*Position `json:"positions"`
}

type Order struct {
	ID        string  `json:"id"`
	Market    string  `json:"market"`
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Qty       float64 `json:"qty"`
	Price     float64 `json:"price"`
	Notional  float64 `json:"notional"`
	Fee       float64 `json:"fee"`
	Reason    string  `json:"reason"`
	CreatedAt int64   `json:"created_at"`
}

type Broker struct {
	mu       sync.Mutex
	accounts map[string]*Account
	orders   []*Order
	seq      int64
}

func NewBroker(initial map[string]float64) *Broker {
	b := &Broker{accounts: map[string]*Account{}, orders: []*Order{}}
	for _, m := range []string{"crypto", "ashare"} {
		cash := initial[m]
		if cash <= 0 {
			cash = 10000
		}
		b.accounts[m] = &Account{Market: m, Cash: cash, InitialCash: cash, Positions: map[string]*Position{}}
	}
	return b
}

func (b *Broker) Account(market string) *Account {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneAccount(b.accounts[market])
}

func cloneAccount(a *Account) *Account {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Positions = map[string]*Position{}
	for k, v := range a.Positions {
		p := *v
		cp.Positions[k] = &p
	}
	return &cp
}

func (b *Broker) Mark(symbol string, price float64) {
	if price <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.accounts {
		if p, ok := a.Positions[symbol]; ok {
			p.Last = price
		}
	}
}

func (b *Broker) equityLocked(market string) float64 {
	a := b.accounts[market]
	if a == nil {
		return 0
	}
	eq := a.Cash
	for _, p := range a.Positions {
		eq += p.Qty * p.Last
	}
	return eq
}

func (b *Broker) Equity(market string) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.equityLocked(market)
}

type feeModel struct {
	feeBps, stampBps, slipBps, lot, minFee float64
}

func feeFor(market string) feeModel {
	if market == "ashare" {
		return feeModel{feeBps: 2.5, stampBps: 5, slipBps: 5, lot: 100, minFee: 5}
	}
	return feeModel{feeBps: 10, slipBps: 2, lot: 0.000001}
}

func roundLot(q, lot float64) float64 {
	if lot <= 0 {
		return q
	}
	if lot >= 1 {
		return math.Floor(q/lot) * lot
	}
	return math.Floor(q*1e6) / 1e6
}

func feeOf(notional float64, side string, fm feeModel) float64 {
	fee := notional * fm.feeBps / 10000
	if fee < fm.minFee {
		fee = fm.minFee
	}
	if side == "sell" {
		fee += notional * fm.stampBps / 10000
	}
	return fee
}

// Execute 按目标仓位（占净值比例）调仓。
func (b *Broker) Execute(symbol string, targetPct, price float64, reason string) (*Order, error) {
	if price <= 0 {
		return nil, errors.New("价格无效")
	}
	sym, err := ParseSymbol(symbol)
	if err != nil {
		return nil, err
	}
	market := sym.Market()
	fm := feeFor(market)

	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.accounts[market]
	if a == nil {
		return nil, fmt.Errorf("未知市场 %s", market)
	}
	eq := b.equityLocked(market)
	targetQty := roundLot(targetPct*eq/price, fm.lot)
	curQty := 0.0
	if p := a.Positions[symbol]; p != nil {
		curQty = p.Qty
	}
	delta := targetQty - curQty
	if math.Abs(delta*price) < 1 {
		return nil, fmt.Errorf("调整金额过小")
	}
	side := "buy"
	if delta < 0 {
		side = "sell"
	}
	qty := roundLot(math.Abs(delta), fm.lot)
	if qty <= 0 {
		return nil, fmt.Errorf("不足最小交易单位")
	}
	px := price * (1 + fm.slipBps/10000)
	if side == "sell" {
		px = price * (1 - fm.slipBps/10000)
	}
	notional := qty * px
	fee := feeOf(notional, side, fm)
	if side == "buy" {
		if notional+fee > a.Cash {
			return nil, fmt.Errorf("资金不足：需要 %.2f，可用 %.2f", notional+fee, a.Cash)
		}
		p := a.Positions[symbol]
		if p == nil {
			p = &Position{Symbol: symbol, OpenedAt: time.Now().Unix()}
			a.Positions[symbol] = p
		}
		newQty := p.Qty + qty
		p.AvgCost = (p.AvgCost*p.Qty + notional + fee) / newQty
		p.Qty = newQty
		p.Last = px
		a.Cash -= notional + fee
		a.FeesPaid += fee
	} else {
		p := a.Positions[symbol]
		if p == nil || p.Qty <= 0 {
			return nil, errors.New("没有可卖持仓")
		}
		if qty > p.Qty {
			qty = roundLot(p.Qty, fm.lot)
			notional = qty * px
			fee = feeOf(notional, side, fm)
		}
		a.Cash += notional - fee
		a.FeesPaid += fee
		a.RealizedPnL += (px-p.AvgCost)*qty - fee
		p.Qty -= qty
		p.Last = px
		if p.Qty <= 1e-9 {
			delete(a.Positions, symbol)
		}
	}
	b.seq++
	o := &Order{ID: fmt.Sprintf("o%d", b.seq), Market: market, Symbol: symbol, Side: side, Qty: qty,
		Price: px, Notional: notional, Fee: fee, Reason: reason, CreatedAt: time.Now().Unix()}
	b.orders = append(b.orders, o)
	if len(b.orders) > 500 {
		b.orders = b.orders[len(b.orders)-500:]
	}
	return o, nil
}

func (b *Broker) Orders(limit int) []*Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit <= 0 || limit > len(b.orders) {
		limit = len(b.orders)
	}
	out := []*Order{}
	for i := len(b.orders) - 1; i >= 0 && len(out) < limit; i-- {
		cp := *b.orders[i]
		out = append(out, &cp)
	}
	return out
}

func (b *Broker) PositionsView(market string) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.accounts[market]
	out := []map[string]any{}
	if a == nil {
		return out
	}
	for _, p := range a.Positions {
		out = append(out, map[string]any{
			"symbol": p.Symbol, "qty": p.Qty, "avg_cost": p.AvgCost, "last_price": p.Last,
			"market_value": p.Qty * p.Last, "unrealized_pnl": (p.Last - p.AvgCost) * p.Qty,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["market_value"].(float64) > out[j]["market_value"].(float64) })
	return out
}

func (b *Broker) Export() map[string]*Account {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]*Account{}
	for k, v := range b.accounts {
		out[k] = cloneAccount(v)
	}
	return out
}

func (b *Broker) Import(in map[string]*Account) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range in {
		if v == nil {
			continue
		}
		if v.Positions == nil {
			v.Positions = map[string]*Position{}
		}
		b.accounts[k] = v
	}
}

func (b *Broker) ExportOrders() []*Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Order, len(b.orders))
	copy(out, b.orders)
	return out
}

func (b *Broker) ImportOrders(orders []*Order) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.orders = orders
	for _, o := range orders {
		if o.ID > fmt.Sprintf("o%d", b.seq) {
			var n int64
			fmt.Sscanf(o.ID, "o%d", &n)
			if n > b.seq {
				b.seq = n
			}
		}
	}
}
