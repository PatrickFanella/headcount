// Package detect holds the pure detector functions. Every series is sampled
// once per minute; NaN marks a missing minute.
package detect

import (
	"math"
	"slices"
)

var nan = math.NaN()

// HeldToNaN removes repeated values only for legacy sensitivity diagnostics.
// Equal observations do not establish staleness; live scoring retains them.
func HeldToNaN(v []float64) []float64 {
	out := slices.Clone(v)
	last := nan
	for i, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if x == last {
			out[i] = nan
		}
		last = x
	}
	return out
}

// Interpolate fills NaN gaps linearly. Leading and trailing gaps take the
// nearest value. It returns nil if there are no values at all.
func Interpolate(v []float64) []float64 {
	out := slices.Clone(v)
	prev := -1
	for i, x := range v {
		if math.IsNaN(x) {
			continue
		}
		if prev == -1 {
			for j := range i {
				out[j] = x
			}
		} else if i-prev > 1 {
			a, b := v[prev], x
			for j := prev + 1; j < i; j++ {
				f := float64(j-prev) / float64(i-prev)
				out[j] = a + f*(b-a)
			}
		}
		prev = i
	}
	if prev == -1 {
		return nil
	}
	for j := prev + 1; j < len(v); j++ {
		out[j] = v[prev]
	}
	return out
}

// Coverage is the fraction of non-NaN values.
func Coverage(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	n := 0
	for _, x := range v {
		if !math.IsNaN(x) {
			n++
		}
	}
	return float64(n) / float64(len(v))
}

// RollingMedian computes a centered median with the given odd window,
// truncated at the edges.
func RollingMedian(v []float64, window int) []float64 {
	half := window / 2
	out := make([]float64, len(v))
	buf := make([]float64, 0, window)
	for i := range v {
		buf = buf[:0]
		for j := max(0, i-half); j <= min(len(v)-1, i+half); j++ {
			if !math.IsNaN(v[j]) {
				buf = append(buf, v[j])
			}
		}
		out[i] = Median(buf)
	}
	return out
}

// Median of the non-NaN values; NaN if none. The input is not modified.
func Median(v []float64) float64 {
	return Quantile(v, 0.5)
}

// Quantile with linear interpolation over the non-NaN values.
func Quantile(v []float64, q float64) float64 {
	s := make([]float64, 0, len(v))
	for _, x := range v {
		if !math.IsNaN(x) {
			s = append(s, x)
		}
	}
	if len(s) == 0 {
		return nan
	}
	slices.Sort(s)
	pos := q * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (pos-float64(lo))*(s[hi]-s[lo])
}

func Mean(v []float64) float64 {
	sum, n := 0.0, 0
	for _, x := range v {
		if !math.IsNaN(x) {
			sum += x
			n++
		}
	}
	if n == 0 {
		return nan
	}
	return sum / float64(n)
}

// MAD is the median absolute deviation, scaled to match a normal standard
// deviation.
func MAD(v []float64) float64 {
	m := Median(v)
	dev := make([]float64, 0, len(v))
	for _, x := range v {
		if !math.IsNaN(x) {
			dev = append(dev, math.Abs(x-m))
		}
	}
	return 1.4826 * Median(dev)
}

// Skewness (population) of the non-NaN values.
func Skewness(v []float64) float64 {
	m := Mean(v)
	var m2, m3 float64
	n := 0
	for _, x := range v {
		if math.IsNaN(x) {
			continue
		}
		d := x - m
		m2 += d * d
		m3 += d * d * d
		n++
	}
	if n < 3 || m2 == 0 {
		return 0
	}
	m2 /= float64(n)
	m3 /= float64(n)
	return m3 / math.Pow(m2, 1.5)
}

// Autocorrelation of v at the given lag; v must not contain NaN.
func Autocorrelation(v []float64, lag int) float64 {
	n := len(v)
	if lag >= n {
		return 0
	}
	m := Mean(v)
	var num, den float64
	for i := range n {
		d := v[i] - m
		if math.IsNaN(d) {
			continue
		}
		den += d * d
		if i+lag < n && !math.IsNaN(v[i+lag]) {
			num += d * (v[i+lag] - m)
		}
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// ramp maps x linearly from [lo, hi] onto [0, 1], clamped. lo may exceed hi
// for decreasing ramps.
func ramp(x, lo, hi float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	f := (x - lo) / (hi - lo)
	return math.Max(0, math.Min(1, f))
}
