package research

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/detect"
	"git.subcult.tv/subculture-collective/headcount/internal/score"
)

var splits = []string{"development", "validation", "calibration", "test"}
var targets = []string{"audience", "chat", "follows"}
var labels = []string{"consistent", "ordinary", "authorized", "indeterminate"}

type Review struct {
	Reviewer string            `json:"reviewer"`
	Labels   map[string]string `json:"labels"`
	Sources  []string          `json:"sources"`
	Notes    string            `json:"notes"`
}
type Case struct {
	Nomination           *ControlNomination `json:"nomination,omitempty"`
	ID                   string             `json:"id"`
	Input                score.Snapshot     `json:"input"`
	Digest               string             `json:"input_sha256"`
	Split                string             `json:"split"`
	Campaign             string             `json:"campaign,omitempty"`
	Stratum              string             `json:"stratum"`
	InclusionProbability float64            `json:"inclusion_probability"`
	Challenge            bool               `json:"challenge"`
	Reviews              []Review           `json:"reviews"`
	Adjudication         *Review            `json:"adjudication,omitempty"`
}

// BlindCase omits all scorer outputs and score-derived shared-account evidence.
// The original digest identifies the frozen packet; reviewers see observations.
type BlindCase struct {
	ID       string `json:"id"`
	Stream   any    `json:"stream"`
	Series   any    `json:"series"`
	Accounts any    `json:"accounts"`
	Behavior any    `json:"behavior"`
	Review   Review `json:"review"`
}

func Blind(p Packet) BlindCase {
	return BlindCase{ID: p.Digest, Stream: p.Input.Ref, Series: p.Input.Series, Accounts: p.Input.Accounts, Behavior: p.Input.Behavior, Review: Review{Labels: map[string]string{"audience": "indeterminate", "chat": "indeterminate", "follows": "indeterminate"}, Sources: []string{}}}
}

// Sample selects one snapshot per stream, without looking at its quotient.
// Its frame is the supplied snapshot pool, never all Twitch.
func Sample(pool []Packet, n int, seed uint64) ([]Case, error) {
	latest := map[int64]Packet{}
	for _, p := range pool {
		if err := p.Verify(); err != nil {
			return nil, err
		}
		old, ok := latest[p.Input.Ref.ID]
		if !ok || p.Input.ObservedAt.After(old.Input.ObservedAt) {
			latest[p.Input.Ref.ID] = p
		}
	}
	if n <= 0 || n > len(latest) {
		return nil, fmt.Errorf("sample size must be in [1,%d]", len(latest))
	}
	ids := make([]int64, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	rng := rand.New(rand.NewPCG(seed, 1))
	rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	out := make([]Case, 0, n)
	for _, id := range ids[:n] {
		p := latest[id]
		out = append(out, Case{ID: p.Digest, Input: p.Input, Digest: p.Digest, Split: "development", Stratum: "snapshot_pool", InclusionProbability: float64(n) / float64(len(latest)), Reviews: []Review{}})
	}
	return out, nil
}

func ValidateCases(cs []Case) error {
	channels := map[int64]string{}
	streams := map[int64]string{}
	campaigns := map[string]string{}
	ids := map[string]bool{}
	first, last := map[string]time.Time{}, map[string]time.Time{}
	for _, c := range cs {
		if ids[c.ID] || c.ID == "" {
			return fmt.Errorf("duplicate or empty case id %q", c.ID)
		}
		ids[c.ID] = true
		if err := (Packet{Input: c.Input, Digest: c.Digest}).Verify(); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
		if !slices.Contains(splits, c.Split) {
			return fmt.Errorf("unknown split %q", c.Split)
		}
		if !(c.InclusionProbability > 0 && c.InclusionProbability <= 1) {
			return fmt.Errorf("case %s: inclusion probability must be in (0,1]", c.ID)
		}
		if prev, ok := channels[c.Input.Ref.ChannelID]; ok && prev != c.Split {
			return fmt.Errorf("channel crosses splits")
		}
		channels[c.Input.Ref.ChannelID] = c.Split
		if prev, ok := streams[c.Input.Ref.ID]; ok && prev != c.Split {
			return fmt.Errorf("stream crosses splits")
		}
		streams[c.Input.Ref.ID] = c.Split
		if c.Campaign != "" {
			if prev, ok := campaigns[c.Campaign]; ok && prev != c.Split {
				return fmt.Errorf("campaign crosses splits")
			}
			campaigns[c.Campaign] = c.Split
		}
		if first[c.Split].IsZero() || c.Input.Ref.FirstSeenAt.Before(first[c.Split]) {
			first[c.Split] = c.Input.Ref.FirstSeenAt
		}
		latestEvidence := maxTime(c.Input.Ref.LastSeenAt, c.Input.ObservedAt)
		if latestEvidence.After(last[c.Split]) {
			last[c.Split] = latestEvidence
		}
		reviewers := map[string]bool{}
		for _, r := range c.Reviews {
			if r.Reviewer == "" || reviewers[r.Reviewer] || noSources(r.Sources) {
				return fmt.Errorf("case %s: independent reviewer IDs and sources required", c.ID)
			}
			reviewers[r.Reviewer] = true
			if err := validReview(r); err != nil {
				return err
			}
		}
		if c.Adjudication != nil {
			if c.Adjudication.Reviewer == "" || noSources(c.Adjudication.Sources) {
				return fmt.Errorf("adjudication needs provenance")
			}
			if err := validReview(*c.Adjudication); err != nil {
				return err
			}
		}
	}
	for i, a := range splits {
		for _, b := range splits[i+1:] {
			if !last[a].IsZero() && !first[b].IsZero() && !last[a].Before(first[b]) {
				return fmt.Errorf("%s and %s are not disjoint forward-time periods", a, b)
			}
		}
	}
	return nil
}
func validReview(r Review) error {
	for _, target := range targets {
		if !slices.Contains(labels, r.Labels[target]) {
			return fmt.Errorf("review %s: invalid %s label", r.Reviewer, target)
		}
	}
	return nil
}

func agreed(c Case, target string) (positive, known bool) {
	if len(c.Reviews) < 2 {
		return false, false
	}
	label := c.Reviews[0].Labels[target]
	for _, r := range c.Reviews[1:] {
		if r.Labels[target] != label {
			if c.Adjudication == nil {
				return false, false
			}
			label = c.Adjudication.Labels[target]
			break
		}
	}
	if label == "indeterminate" {
		return false, false
	}
	return label == "consistent", true
}

type Observation struct {
	CaseID    string   `json:"case_id"`
	Group     string   `json:"group"`
	Stratum   string   `json:"stratum"`
	Score     float64  `json:"score"`
	Coverage  float64  `json:"coverage"`
	Positive  bool     `json:"positive"`
	Known     bool     `json:"known"`
	Weight    float64  `json:"weight"`
	Hours     float64  `json:"hours"`
	Challenge bool     `json:"challenge"`
	Tags      []string `json:"tags"`
}

func Observe(cs []Case, split, target string, omit []string) ([]Observation, error) {
	if err := ValidateCases(cs); err != nil {
		return nil, err
	}
	if !slices.Contains(targets, target) {
		return nil, fmt.Errorf("unknown target %s", target)
	}
	parent := map[string]string{}
	var root func(string) string
	root = func(x string) string {
		p, ok := parent[x]
		if !ok {
			parent[x] = x
			return x
		}
		if p != x {
			parent[x] = root(p)
		}
		return parent[x]
	}
	for _, c := range cs {
		ch := fmt.Sprintf("channel:%d", c.Input.Ref.ChannelID)
		root(ch)
		if c.Campaign != "" {
			a, b := root(ch), root("campaign:"+c.Campaign)
			if a != b {
				parent[b] = a
			}
		}
	}
	var out []Observation
	for _, c := range cs {
		if c.Split != split {
			continue
		}
		r := score.Evaluate(c.Input, omit)
		m := r.Evidence.Mechanisms[target]
		positive, known := agreed(c, target)
		group := root(fmt.Sprintf("channel:%d", c.Input.Ref.ChannelID))
		coverage := r.Coverage
		if !m.Available || r.Evidence.Status == "insufficient_evidence" {
			coverage = 0
		}
		weight := 1 / c.InclusionProbability
		if c.Challenge {
			weight = 1
		}
		// Deduplicate stream snapshots when estimating alert workload. Multiple
		// snapshots belong in a separate persistence study, not independent trials.
		viewers := make([]float64, 0, len(c.Input.Series.Viewers))
		for _, v := range c.Input.Series.Viewers {
			viewers = append(viewers, float64(v.Viewers))
		}
		size := "under_1k"
		if median := detect.Median(viewers); median >= 10000 {
			size = "10k_plus"
		} else if median >= 1000 {
			size = "1k_to_10k"
		}
		language, category := c.Input.Ref.Language, c.Input.GameName
		if language == "" {
			language = "unknown"
		}
		if category == "" {
			category = "unknown"
		}
		quality := "under_40pct"
		if coverage >= 0.4 {
			quality = "40pct_plus"
		}
		tags := []string{"language:" + language, "category:" + category, "audience:" + size, "coverage:" + quality, "stratum:" + c.Stratum}
		out = append(out, Observation{CaseID: c.ID, Group: group, Stratum: c.Stratum, Tags: tags, Score: m.Evidence, Coverage: coverage, Positive: positive, Known: known, Weight: weight, Hours: c.Input.Ref.LastSeenAt.Sub(c.Input.Ref.FirstSeenAt).Hours(), Challenge: c.Challenge})
	}
	return out, nil
}

type Metrics struct {
	Threshold            float64  `json:"threshold"`
	Cases                int      `json:"cases"`
	Reviewed             int      `json:"reviewed"`
	Abstained            int      `json:"abstained"`
	Alerts               int      `json:"alerts"`
	Precision            *float64 `json:"precision"`
	Recall               *float64 `json:"recall"`
	FalseAlertsPerStream *float64 `json:"false_alerts_per_stream"`
	FalseAlertsPerHour   *float64 `json:"false_alerts_per_hour"`
	Unreviewed           int      `json:"unreviewed"`
	UnreviewedAlerts     int      `json:"unreviewed_alerts"`
}

func ratio(a, b float64) *float64 {
	if b <= 0 {
		return nil
	}
	v := a / b
	return &v
}
func Measure(os []Observation, threshold, coverage float64) Metrics {
	m := Metrics{Threshold: threshold}
	var tp, fp, positives, total, hours float64
	for _, o := range os {
		m.Cases++
		alert := o.Coverage >= coverage && o.Score >= threshold
		if alert {
			m.Alerts++
		}
		if !o.Known {
			m.Unreviewed++
			if alert {
				m.UnreviewedAlerts++
			}
			continue
		}
		m.Reviewed++
		total += o.Weight
		hours += o.Weight * o.Hours
		if o.Positive {
			positives += o.Weight
		}
		if o.Coverage < coverage {
			m.Abstained++
			continue
		}
		if o.Score >= threshold {
			if o.Positive {
				tp += o.Weight
			} else {
				fp += o.Weight
			}
		}
	}
	m.Precision = ratio(tp, tp+fp)
	m.Recall = ratio(tp, positives)
	m.FalseAlertsPerStream = ratio(fp, total)
	m.FalseAlertsPerHour = ratio(fp, hours)
	return m
}

// PrecisionInterval resamples connected groups rather than individual minutes.
// Campaign/channel group construction should conservatively merge shared
// operations before use. This interval is descriptive for the reviewed sample.
func PrecisionInterval(os []Observation, threshold, coverage float64) [2]*float64 {
	groups := map[string][]Observation{}
	for _, o := range os {
		groups[o.Group] = append(groups[o.Group], o)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) < 2 {
		return [2]*float64{}
	}
	rng := rand.New(rand.NewPCG(17, 1))
	var values []float64
	for range 500 {
		var sample []Observation
		for range keys {
			sample = append(sample, groups[keys[rng.IntN(len(keys))]]...)
		}
		m := Measure(sample, threshold, coverage)
		if m.Precision != nil {
			values = append(values, *m.Precision)
		}
	}
	if len(values) < 100 {
		return [2]*float64{}
	}
	slices.Sort(values)
	lo, hi := values[len(values)*25/1000], values[len(values)*975/1000]
	return [2]*float64{&lo, &hi}
}

type Report struct {
	Target            string             `json:"target"`
	Split             string             `json:"split"`
	CoverageFloor     float64            `json:"coverage_floor"`
	Representative    Metrics            `json:"representative"`
	Challenge         Metrics            `json:"challenge"`
	PrecisionInterval [2]*float64        `json:"representative_precision_interval"`
	PR                []Metrics          `json:"precision_recall"`
	Subgroups         map[string]Metrics `json:"subgroups"`
	Note              string             `json:"note"`
}

func Evaluate(cs []Case, split, target string, threshold, coverage float64, omit []string) (Report, error) {
	if threshold < 0 || threshold > 1 || coverage <= 0 || coverage > 1 || math.IsNaN(threshold) || math.IsNaN(coverage) {
		return Report{}, fmt.Errorf("threshold must be in [0,1], coverage in (0,1]")
	}
	os, err := Observe(cs, split, target, omit)
	if err != nil {
		return Report{}, err
	}
	if len(os) == 0 {
		return Report{}, fmt.Errorf("no cases in requested split")
	}
	var rep, challenge []Observation
	groups := map[string][]Observation{}
	seen := map[int64]bool{}
	for _, c := range cs {
		if c.Split == split {
			if seen[c.Input.Ref.ID] {
				return Report{}, fmt.Errorf("evaluation requires one snapshot per stream; repeated snapshots are correlated")
			}
			seen[c.Input.Ref.ID] = true
		}
	}
	for _, o := range os {
		if o.Challenge {
			challenge = append(challenge, o)
		} else {
			rep = append(rep, o)
			for _, tag := range o.Tags {
				groups[tag] = append(groups[tag], o)
			}
		}
	}
	report := Report{Target: target, Split: split, CoverageFloor: coverage, Representative: Measure(rep, threshold, coverage), Challenge: Measure(challenge, threshold, coverage), PrecisionInterval: PrecisionInterval(rep, threshold, coverage), Subgroups: map[string]Metrics{}, Note: "Scores are uncalibrated evidence indices. Indeterminate/unreviewed cases are excluded from accuracy metrics; abstentions count against recall. Population is the supplied sampling frame. Repeated alert persistence requires a separate chronological replay."}
	levels := []float64{0, 1}
	for _, o := range rep {
		levels = append(levels, o.Score)
	}
	slices.Sort(levels)
	levels = slices.Compact(levels)
	for _, x := range levels {
		report.PR = append(report.PR, Measure(rep, x, coverage))
	}
	for k, g := range groups {
		report.Subgroups[k] = Measure(g, threshold, coverage)
	}
	return report, nil
}

func noSources(sources []string) bool {
	if len(sources) == 0 {
		return true
	}
	for _, s := range sources {
		if strings.TrimSpace(s) == "" {
			return true
		}
	}
	return false
}
