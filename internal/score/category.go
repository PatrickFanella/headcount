package score

import (
	"math"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

// Chat engagement differs by category: a Just Chatting audience talks far
// more than one watching a tournament. Each stream is compared with the
// language baseline adjusted by an offset for its category family and then its
// own category. Offsets are shrunk towards zero when few peer streams exist,
// so a small category borrows from similar ones, and they are capped so a
// category full of botted streams cannot make low engagement look normal.
const (
	offsetPrior = 10  // pseudo-streams pulling an offset towards zero
	maxOffsetSD = 1.5 // largest offset, in residual standard deviations
)

// families groups non-game categories with similar chat behaviour. Every
// other category is a game.
var families = map[string]string{
	"Just Chatting": "social", "IRL": "social", "Talk Shows & Podcasts": "social", "ASMR": "social",
	"Pools, Hot Tubs, and Beaches": "social", "Travel & Outdoors": "social", "Food & Drink": "social",
	"Fitness & Health": "social", "Politics": "social", "Special Events": "social",
	"Animals, Aquariums, and Zoos": "social", "Beauty & Body Art": "social",

	"Music": "creative", "DJs": "creative", "Art": "creative", "Makers & Crafting": "creative",
	"Science & Technology": "creative", "Software and Game Development": "creative",

	"Slots": "gambling", "Virtual Casino": "gambling", "Poker": "gambling",

	"Sports": "sports", "Chess": "sports",
}

func familyOf(gameName string) string {
	if f, ok := families[gameName]; ok {
		return f
	}
	return "games"
}

type offsets struct {
	family   map[string]float64
	famN     map[string]int
	category map[string]float64
	catN     map[string]int
	names    map[string]string // category id → name
}

// Expectation is the baseline for one stream.
type Expectation struct {
	line           *baseline
	PerLanguage    bool
	Family         string
	FamilyOffset   float64
	FamilyPeers    int
	Category       string
	CategoryOffset float64
	CategoryPeers  int
}

func (e Expectation) mean(viewers float64) float64 {
	return e.line.a + e.line.slope*math.Log(viewers) + e.FamilyOffset + e.CategoryOffset
}

func (e Expectation) Expected(viewers float64) float64 { return math.Exp(e.mean(viewers)) - 0.1 }

func (e Expectation) Z(viewers, perK float64) float64 {
	return (math.Log(perK+0.1) - e.mean(viewers)) / e.line.sd
}

// fitOffsets estimates family and category offsets from residuals against
// each peer's language line. Outliers are left out so botted streams do not
// shift their category.
func (b *baselines) fitOffsets(peers []store.PeerStat) {
	o := &offsets{family: map[string]float64{}, famN: map[string]int{},
		category: map[string]float64{}, catN: map[string]int{}, names: map[string]string{}}
	type res struct {
		fam, cat string
		r        float64
	}
	var rs []res
	for _, p := range peers {
		if p.MedianViewers <= 0 {
			continue
		}
		line, _ := b.For(p.Language)
		if !line.ok {
			continue
		}
		r := (math.Log(p.ChattersPerK+0.1) - (line.a + line.slope*math.Log(p.MedianViewers))) / line.sd
		if math.Abs(r) > 2.5 {
			continue
		}
		if p.GameID != "" {
			o.names[p.GameID] = p.GameName
		}
		rs = append(rs, res{fam: familyOf(p.GameName), cat: p.GameID, r: r})
	}
	// Offsets are estimated in SD units, then capped and stored as such.
	famSum := map[string]float64{}
	for _, x := range rs {
		famSum[x.fam] += x.r
		o.famN[x.fam]++
	}
	for f, s := range famSum {
		o.family[f] = clamp(s/float64(o.famN[f]+offsetPrior), maxOffsetSD)
	}
	catSum := map[string]float64{}
	for _, x := range rs {
		if x.cat == "" {
			continue
		}
		catSum[x.cat] += x.r - o.family[x.fam]
		o.catN[x.cat]++
	}
	for c, s := range catSum {
		o.category[c] = clamp(s/float64(o.catN[c]+offsetPrior), maxOffsetSD)
	}
	b.offsets = o
}

// Expect returns the baseline for a stream's language and main category.
func (b *baselines) Expect(lang, gameID, gameName string) Expectation {
	line, perLang := b.For(lang)
	e := Expectation{line: line, PerLanguage: perLang, Category: gameName}
	if b.offsets == nil {
		return e
	}
	if gameName == "" {
		gameName = b.offsets.names[gameID]
		e.Category = gameName
	}
	e.Family = familyOf(gameName)
	total := clamp(b.offsets.family[e.Family]+b.offsets.category[gameID], maxOffsetSD)
	e.FamilyOffset = b.offsets.family[e.Family] * line.sd
	e.CategoryOffset = (total - b.offsets.family[e.Family]) * line.sd
	e.FamilyPeers = b.offsets.famN[e.Family]
	e.CategoryPeers = b.offsets.catN[gameID]
	return e
}

func clamp(x, lim float64) float64 { return math.Max(-lim, math.Min(lim, x)) }
