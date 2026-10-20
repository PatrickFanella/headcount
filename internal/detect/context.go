package detect

// FollowerInterval is an observed net change. Neither events nor event times
// inside the interval are identifiable from totals.
type FollowerInterval struct {
	End, Minutes int
	Delta        float64
}

func FollowerIntervals(v []float64) []FollowerInterval {
	var out []FollowerInterval
	prev := -1
	for i, x := range v {
		if !finite(x) {
			continue
		}
		if prev >= 0 {
			out = append(out, FollowerInterval{i, i - prev, x - v[prev]})
		}
		prev = i
	}
	return out
}

func restrictedAt(d StreamData, i int) bool {
	if len(d.RestrictedMinutes) > 0 {
		return i < len(d.RestrictedMinutes) && d.RestrictedMinutes[i]
	}
	return d.RestrictedChat
}

func contextAt(d StreamData, from, to int) bool {
	for i := max(0, from); i < to; i++ {
		if i < len(d.ContextMinutes) && d.ContextMinutes[i] {
			return true
		}
	}
	return false
}

func restrictedRange(d StreamData, from, to int) bool {
	for i := max(0, from); i < to; i++ {
		if restrictedAt(d, i) {
			return true
		}
	}
	return false
}
