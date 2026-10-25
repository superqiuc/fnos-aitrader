package main

import "math"

func sma(bars []Bar, n int) float64 {
	if n <= 0 || len(bars) < n {
		return 0
	}
	sum := 0.0
	for _, b := range bars[len(bars)-n:] {
		sum += b.Close
	}
	return sum / float64(n)
}

func rsi(bars []Bar, n int) float64 {
	if len(bars) < n+1 {
		return 50
	}
	var gain, loss float64
	for i := len(bars) - n; i < len(bars); i++ {
		d := bars[i].Close - bars[i-1].Close
		if d > 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	if loss == 0 {
		return 100
	}
	rs := (gain / float64(n)) / (loss / float64(n))
	return 100 - 100/(1+rs)
}

func atr(bars []Bar, n int) float64 {
	if len(bars) < n+1 {
		return 0
	}
	sum := 0.0
	for i := len(bars) - n; i < len(bars); i++ {
		tr := bars[i].High - bars[i].Low
		prev := bars[i-1].Close
		tr = math.Max(tr, math.Abs(bars[i].High-prev))
		tr = math.Max(tr, math.Abs(bars[i].Low-prev))
		sum += tr
	}
	return sum / float64(n)
}

func pctChange(bars []Bar, n int) float64 {
	if len(bars) < n+1 {
		return 0
	}
	prev := bars[len(bars)-1-n].Close
	if prev <= 0 {
		return 0
	}
	return (bars[len(bars)-1].Close/prev - 1) * 100
}

type Snapshot struct {
	Symbol   string
	Price    float64
	MA20     float64
	MA60     float64
	RSI14    float64
	ATRPct   float64
	Return5  float64
	Return20 float64
	Bars     int
}

func buildSnapshot(symbol string, bars []Bar) Snapshot {
	s := Snapshot{Symbol: symbol, Bars: len(bars)}
	if len(bars) == 0 {
		return s
	}
	s.Price = bars[len(bars)-1].Close
	s.MA20 = sma(bars, 20)
	s.MA60 = sma(bars, 60)
	s.RSI14 = rsi(bars, 14)
	if s.Price > 0 {
		s.ATRPct = atr(bars, 14) / s.Price * 100
	}
	s.Return5 = pctChange(bars, 5)
	s.Return20 = pctChange(bars, 20)
	return s
}
