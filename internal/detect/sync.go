package detect

import "math"

// SpikeOnsets returns minute indices where viewers rise above the baseline by
// at least threshold and stay there for two or more minutes.
func SpikeOnsets(p Prepared, threshold float64) []int {
	var out []int
	for i := 1; i+1 < len(p.Rel); i++ {
		if p.Rel[i] >= threshold && p.Rel[i+1] >= threshold && p.Rel[i-1] < threshold {
			out = append(out, i)
		}
	}
	return out
}

// Partner is another channel's spike onsets (unix minutes) while both were live.
type Partner struct {
	Login          string
	OverlapMinutes int
	Onsets         []int64
}

// binomTail is P(X >= k) for X ~ Binomial(n, p).
func binomTail(n, k int, p float64) float64 {
	if k <= 0 {
		return 1
	}
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 1
	}
	lgN, _ := math.Lgamma(float64(n + 1))
	sum := 0.0
	for i := k; i <= n; i++ {
		lgI, _ := math.Lgamma(float64(i + 1))
		lgNI, _ := math.Lgamma(float64(n - i + 1))
		sum += math.Exp(lgN - lgI - lgNI + float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
	}
	return math.Min(1, sum)
}

// Synchrony tests whether this stream's spike onsets coincide with another
// channel's more often than chance. A view-bot service that rotates bots
// across customers makes several channels spike in the same minutes.
//
// For each partner, the chance that a random onset lands within ±tol minutes
// of one of its onsets is about len(onsets)*(2*tol+1)/overlap. The smallest
// binomial p-value across partners is Bonferroni-corrected for the number of
// partners tested. When many partners are significant at once, the cause is
// probably platform-wide (a Twitch count reset or outage), so the strength
// is reduced.
func Synchrony(own []int64, partners []Partner, tol int) Component {
	if len(own) < 4 {
		return Component{Reason: "fewer than 4 spikes", Available: true, Values: map[string]float64{"own_onsets": float64(len(own))}}
	}
	tested := 0
	bestP := 1.0
	bestK, bestN := 0, len(own)
	bestLogin := ""
	var pvals []float64
	for _, pt := range partners {
		if len(pt.Onsets) < 3 || pt.OverlapMinutes < 60 {
			continue
		}
		tested++
		k := 0
		j := 0
		for _, t := range own {
			for j < len(pt.Onsets) && pt.Onsets[j] < t-int64(tol) {
				j++
			}
			if j < len(pt.Onsets) && pt.Onsets[j] <= t+int64(tol) {
				k++
			}
		}
		p0 := math.Min(1, float64(len(pt.Onsets)*(2*tol+1))/float64(pt.OverlapMinutes))
		pv := binomTail(len(own), k, p0)
		pvals = append(pvals, pv)
		if pv < bestP {
			bestP, bestK, bestLogin = pv, k, pt.Login
		}
	}
	values := map[string]float64{"own_onsets": float64(len(own)), "partners_tested": float64(tested)}
	if tested == 0 {
		return Component{Available: true, Values: values}
	}
	adj := func(p float64) float64 { return math.Min(1, p*float64(tested)) }
	significant := 0
	for _, pv := range pvals {
		if adj(pv) < 0.01 {
			significant++
		}
	}
	bestAdj := adj(bestP)
	strength := ramp(-math.Log10(math.Max(bestAdj, 1e-300)), 2, 6)
	platformWide := significant >= 5 && float64(significant) > 0.25*float64(tested)
	if platformWide {
		strength *= 0.2
	}
	values["best_matches"] = float64(bestK)
	values["best_of"] = float64(bestN)
	values["best_p_adjusted"] = bestAdj
	values["significant_partners"] = float64(significant)
	if platformWide {
		values["platform_wide"] = 1
	}
	c := Component{Available: true, Strength: strength, Values: values}
	if strength > 0 {
		c.Reason = "best partner: " + bestLogin
	}
	return c
}
