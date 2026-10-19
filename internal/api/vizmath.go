package api

import (
	"math"
	"sort"
)

// engagementFit is a log-log line through scored streams:
// log(chatters per 1k + 0.1) = A + Slope*log(median viewers), with residual
// standard deviation SD. It mirrors the scorer's baseline fit (including the
// outlier refit) so the engagement map can draw a comparable line; the
// scorer's own fit uses per-minute peer samples, so the two can differ a little.
type engagementFit struct {
	A     float64 `json:"a"`
	Slope float64 `json:"slope"`
	SD    float64 `json:"sd"`
	N     int     `json:"n"`
}

const (
	minFitGlobal = 20
	minFitLang   = 15
)

// fitEngagement fits the log-log line, or returns false when there are fewer
// than minN usable points or the residuals are degenerate.
func fitEngagement(viewers, perK []float64, minN int) (engagementFit, bool) {
	xs := make([]float64, 0, len(viewers))
	ys := make([]float64, 0, len(viewers))
	for i, v := range viewers {
		if v <= 0 || perK[i] < 0 || math.IsNaN(v) || math.IsNaN(perK[i]) {
			continue
		}
		xs = append(xs, math.Log(v))
		ys = append(ys, math.Log(perK[i]+0.1))
	}
	if len(xs) < minN {
		return engagementFit{}, false
	}
	a, slope := olsFit(xs, ys)
	sd := residSD(xs, ys, a, slope)
	var fx, fy []float64
	for i := range xs {
		if math.Abs(ys[i]-(a+slope*xs[i])) <= 2.5*sd {
			fx = append(fx, xs[i])
			fy = append(fy, ys[i])
		}
	}
	n := len(xs)
	if len(fx) >= minN {
		a, slope = olsFit(fx, fy)
		sd = residSD(fx, fy, a, slope)
		n = len(fx)
	}
	if !(sd > 0) {
		return engagementFit{}, false
	}
	return engagementFit{A: a, Slope: slope, SD: sd, N: n}, true
}

func olsFit(xs, ys []float64) (a, slope float64) {
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
	return (sy - slope*sx) / n, slope
}

func residSD(xs, ys []float64, a, slope float64) float64 {
	if len(xs) < 3 {
		return 0
	}
	var ss float64
	for i := range xs {
		r := ys[i] - (a + slope*xs[i])
		ss += r * r
	}
	return math.Sqrt(ss / float64(len(xs)-2))
}

// onsetPartner is another channel's spike onsets inside a stream's window.
type onsetPartner struct {
	ChannelID      string  `json:"channel_id"`
	Login          string  `json:"login"`
	DisplayName    string  `json:"display_name"`
	Onsets         []int64 `json:"onsets"` // unix seconds
	OverlapMinutes int     `json:"overlap_minutes"`
	Matches        int     `json:"matches"`  // own onsets with one of this channel's within the tolerance
	Expected       float64 `json:"expected"` // matches expected by chance
	Named          bool    `json:"named"`    // the scorer's best synchronized partner
}

// scoreOnsetPartners counts, for each partner, how many of own's onsets
// (unix seconds) have a partner onset within ±tol minutes, and how many such
// matches chance alone would give. The arithmetic follows detect.Synchrony,
// which works on unix minutes.
func scoreOnsetPartners(own []int64, partners []onsetPartner, tol int) {
	ownMin := make([]int64, len(own))
	for i, t := range own {
		ownMin[i] = t / 60
	}
	for i := range partners {
		p := &partners[i]
		pm := make([]int64, len(p.Onsets))
		for j, t := range p.Onsets {
			pm[j] = t / 60
		}
		sort.Slice(pm, func(a, b int) bool { return pm[a] < pm[b] })
		k, j := 0, 0
		for _, t := range ownMin {
			for j < len(pm) && pm[j] < t-int64(tol) {
				j++
			}
			if j < len(pm) && pm[j] <= t+int64(tol) {
				k++
			}
		}
		p.Matches = k
		if p.OverlapMinutes > 0 {
			p0 := math.Min(1, float64(len(pm)*(2*tol+1))/float64(p.OverlapMinutes))
			p.Expected = math.Round(p0*float64(len(own))*100) / 100
		}
	}
}

// pickOnsetPartners keeps the partners the synchrony detector would test
// (three or more onsets, an hour of overlap), ranks them by how far matches
// exceed chance, and returns at most limit with at least one match. A named
// partner is always kept.
func pickOnsetPartners(partners []onsetPartner, named string, limit int) (picked []onsetPartner, tested int) {
	var eligible []onsetPartner
	for _, p := range partners {
		if p.Login == named && named != "" {
			p.Named = true
			picked = append(picked, p)
			if len(p.Onsets) >= 3 && p.OverlapMinutes >= 60 {
				tested++
			}
			continue
		}
		if len(p.Onsets) < 3 || p.OverlapMinutes < 60 {
			continue
		}
		tested++
		if p.Matches > 0 {
			eligible = append(eligible, p)
		}
	}
	sort.SliceStable(eligible, func(a, b int) bool {
		ea := float64(eligible[a].Matches) - eligible[a].Expected
		eb := float64(eligible[b].Matches) - eligible[b].Expected
		if ea != eb {
			return ea > eb
		}
		return eligible[a].Login < eligible[b].Login
	})
	for _, p := range eligible {
		if len(picked) >= limit {
			break
		}
		picked = append(picked, p)
	}
	return picked, tested
}
