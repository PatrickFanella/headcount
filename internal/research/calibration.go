package research

import (
	"fmt"
	"math"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/score"
)

type Knot struct {
	Upper       float64 `json:"upper"`
	Probability float64 `json:"probability"`
}
type Calibration struct {
	Schema        int       `json:"schema"`
	Version       int       `json:"scorer_version"`
	Target        string    `json:"target"`
	CoverageFloor float64   `json:"coverage_floor"`
	Knots         []Knot    `json:"knots"`
	Cases         []string  `json:"calibration_case_ids"`
	Channels      []int64   `json:"channels"`
	Campaigns     []string  `json:"campaigns"`
	End           time.Time `json:"latest_evidence_at"`
	Note          string    `json:"note"`
}

// FitCalibration uses weighted isotonic regression on the designated calibration
// partition. No quotient is interpreted as a probability without such an artifact.
func FitCalibration(cs []Case, target string, floor float64) (Calibration, error) {
	os, err := Observe(cs, "calibration", target, nil)
	if err != nil {
		return Calibration{}, err
	}
	if floor <= 0 || floor > 1 {
		return Calibration{}, fmt.Errorf("coverage floor must be in (0,1]")
	}
	var rows []Observation
	groups := map[string]bool{}
	positives, negatives := 0, 0
	for _, o := range os {
		if o.Known && !o.Challenge && o.Coverage >= floor {
			rows = append(rows, o)
			groups[o.Group] = true
			if o.Positive {
				positives++
			} else {
				negatives++
			}
		}
	}
	if positives == 0 || negatives == 0 || len(groups) < 2 {
		return Calibration{}, fmt.Errorf("calibration requires both classes and at least two independent groups; sufficiency still requires review")
	}
	slices.SortStableFunc(rows, func(a, b Observation) int {
		if a.Score < b.Score {
			return -1
		}
		if a.Score > b.Score {
			return 1
		}
		return 0
	})
	type block struct{ upper, weight, positive float64 }
	var blocks []block
	for _, o := range rows {
		y := 0.0
		if o.Positive {
			y = o.Weight
		}
		b := block{o.Score, o.Weight, y}
		if len(blocks) > 0 && blocks[len(blocks)-1].upper == b.upper {
			last := &blocks[len(blocks)-1]
			last.weight += b.weight
			last.positive += b.positive
		} else {
			blocks = append(blocks, b)
		}
		for len(blocks) > 1 {
			i := len(blocks) - 1
			a, b := blocks[i-1], blocks[i]
			if a.positive/a.weight <= b.positive/b.weight {
				break
			}
			blocks = append(blocks[:i-1], block{b.upper, a.weight + b.weight, a.positive + b.positive})
		}
	}
	out := Calibration{Schema: 1, Version: score.Version, Target: target, CoverageFloor: floor, Note: "Isotonic calibration candidate; accuracy is unestablished until an independent future test. Probability describes the reviewed episode label, not fake viewer share."}
	ids := map[string]bool{}
	for _, o := range rows {
		out.Cases = append(out.Cases, o.CaseID)
		ids[o.CaseID] = true
	}
	for _, c := range cs {
		if !ids[c.ID] {
			continue
		}
		out.Channels = append(out.Channels, c.Input.Ref.ChannelID)
		if c.Campaign != "" {
			out.Campaigns = append(out.Campaigns, c.Campaign)
		}
		out.End = maxTime(out.End, maxTime(c.Input.Ref.LastSeenAt, c.Input.ObservedAt))
	}
	slices.Sort(out.Channels)
	out.Channels = slices.Compact(out.Channels)
	slices.Sort(out.Campaigns)
	out.Campaigns = slices.Compact(out.Campaigns)
	for _, b := range blocks {
		out.Knots = append(out.Knots, Knot{b.upper, b.positive / b.weight})
	}
	return out, nil
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func (c Calibration) probability(x float64) float64 {
	for _, k := range c.Knots {
		if x <= k.Upper {
			return k.Probability
		}
	}
	return c.Knots[len(c.Knots)-1].Probability
}

type Reliability struct {
	Lower     float64  `json:"lower"`
	Count     int      `json:"count"`
	Predicted *float64 `json:"mean_predicted"`
	Observed  *float64 `json:"observed_rate"`
}
type ProbabilityReport struct {
	Evaluated int           `json:"evaluated"`
	Brier     *float64      `json:"brier"`
	LogLoss   *float64      `json:"log_loss"`
	Bins      []Reliability `json:"reliability"`
	Note      string        `json:"note"`
}

func EvaluateCalibration(model Calibration, cs []Case) (ProbabilityReport, error) {
	if model.Schema != 1 || model.Version != score.Version || len(model.Knots) == 0 || model.End.IsZero() || !(model.CoverageFloor > 0 && model.CoverageFloor <= 1) {
		return ProbabilityReport{}, fmt.Errorf("incompatible calibration artifact")
	}
	for i, k := range model.Knots {
		if math.IsNaN(k.Upper) || math.IsNaN(k.Probability) || k.Probability < 0 || k.Probability > 1 || k.Upper < 0 || k.Upper > 1 || (i > 0 && (k.Upper <= model.Knots[i-1].Upper || k.Probability < model.Knots[i-1].Probability)) {
			return ProbabilityReport{}, fmt.Errorf("invalid calibration knots")
		}
	}
	for _, c := range cs {
		if c.Split != "test" {
			continue
		}
		if slices.Contains(model.Channels, c.Input.Ref.ChannelID) || slices.Contains(model.Cases, c.ID) || (c.Campaign != "" && slices.Contains(model.Campaigns, c.Campaign)) || !c.Input.Ref.FirstSeenAt.After(model.End) {
			return ProbabilityReport{}, fmt.Errorf("calibration/test leakage or nonfuture test case")
		}
	}
	rows, err := Observe(cs, "test", model.Target, nil)
	if err != nil {
		return ProbabilityReport{}, err
	}
	out := ProbabilityReport{Note: "Weighted evaluation of independent reviewed test episodes; probabilities concern the label, not audience composition."}
	var total, brier, loss float64
	var weights, preds, ys [10]float64
	var counts [10]int
	for _, o := range rows {
		if !o.Known || o.Challenge || o.Coverage < model.CoverageFloor {
			continue
		}
		p := model.probability(o.Score)
		y := 0.0
		if o.Positive {
			y = 1
		}
		total += o.Weight
		brier += o.Weight * (p - y) * (p - y)
		clipped := math.Max(1e-12, math.Min(1-1e-12, p))
		loss -= o.Weight * (y*math.Log(clipped) + (1-y)*math.Log1p(-clipped))
		out.Evaluated++
		i := min(9, int(p*10))
		weights[i] += o.Weight
		preds[i] += o.Weight * p
		ys[i] += o.Weight * y
		counts[i]++
	}
	if total == 0 {
		return out, fmt.Errorf("no eligible independent test labels")
	}
	out.Brier = ratio(brier, total)
	out.LogLoss = ratio(loss, total)
	for i := range 10 {
		out.Bins = append(out.Bins, Reliability{Lower: float64(i) / 10, Count: counts[i], Predicted: ratio(preds[i], weights[i]), Observed: ratio(ys[i], weights[i])})
	}
	return out, nil
}
