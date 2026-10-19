package score

import (
	"math"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

// baseline models expected chat engagement for a given audience size:
// log(chatters per 1k) = a + slope*log(median viewers). Larger streams have
// proportionally quieter chat, so a fixed ratio would flag every big channel.
type baseline struct {
	ok       bool
	a, slope float64
	sd       float64
}

const minPeers = 20

func fitBaseline(peers []store.PeerStat) *baseline {
	xs := make([]float64, 0, len(peers))
	ys := make([]float64, 0, len(peers))
	for _, p := range peers {
		if p.MedianViewers <= 0 {
			continue
		}
		xs = append(xs, math.Log(p.MedianViewers))
		ys = append(ys, math.Log(p.ChattersPerK+0.1))
	}
	b := &baseline{}
	if len(xs) < minPeers {
		return b
	}
	a, slope := ols(xs, ys)
	// Refit without outliers so heavily botted streams don't drag the
	// baseline down.
	sd := residualSD(xs, ys, a, slope)
	var fx, fy []float64
	for i := range xs {
		if math.Abs(ys[i]-(a+slope*xs[i])) <= 2.5*sd {
			fx = append(fx, xs[i])
			fy = append(fy, ys[i])
		}
	}
	if len(fx) >= minPeers {
		a, slope = ols(fx, fy)
		sd = residualSD(fx, fy, a, slope)
	}
	if sd <= 0 {
		return b
	}
	return &baseline{ok: true, a: a, slope: slope, sd: sd}
}

func ols(xs, ys []float64) (a, slope float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return sy / n, 0
	}
	slope = (n*sxy - sx*sy) / den
	a = (sy - slope*sx) / n
	return
}

func residualSD(xs, ys []float64, a, slope float64) float64 {
	var ss float64
	for i := range xs {
		r := ys[i] - (a + slope*xs[i])
		ss += r * r
	}
	if len(xs) < 3 {
		return 0
	}
	return math.Sqrt(ss / float64(len(xs)-2))
}

func (b *baseline) expected(viewers float64) float64 {
	return math.Exp(b.a+b.slope*math.Log(viewers)) - 0.1
}

func (b *baseline) z(viewers, perK float64) float64 {
	return (math.Log(perK+0.1) - (b.a + b.slope*math.Log(viewers))) / b.sd
}
