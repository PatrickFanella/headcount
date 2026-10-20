package research

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type ControlRoster struct {
	Schema            int                `json:"schema"`
	Source            string             `json:"source"`
	IdentitySource    string             `json:"identity_source"`
	IdentityCheckedOn string             `json:"identity_checked_on"`
	Candidates        []ControlCandidate `json:"candidates"`
}
type ControlCandidate struct {
	Login          string `json:"login"`
	RequestedLogin string `json:"requested_login,omitempty"`
	ChannelID      int64  `json:"channel_id,omitempty"`
	Status         string `json:"status"`
	SuggestedLogin string `json:"suggested_login,omitempty"`
}
type ControlNomination struct {
	Login  string `json:"login"`
	Source string `json:"source"`
	Status string `json:"status"`
}

var twitchLogin = regexp.MustCompile(`^[a-z0-9_]+$`)

// ControlCases selects review candidates by verified channel ID. Nomination
// supplies no class label, weight change, score exemption or reviewer evidence.
func ControlCases(pool []Packet, roster ControlRoster) ([]Case, error) {
	if roster.Schema != 1 || strings.TrimSpace(roster.Source) == "" {
		return nil, fmt.Errorf("control roster needs schema 1 and nomination provenance")
	}
	candidates := map[int64]ControlCandidate{}
	logins := map[string]bool{}
	for _, c := range roster.Candidates {
		if !twitchLogin.MatchString(c.Login) || logins[c.Login] {
			return nil, fmt.Errorf("invalid or duplicate control login %q", c.Login)
		}
		logins[c.Login] = true
		switch c.Status {
		case "unresolved":
			if c.ChannelID != 0 {
				return nil, fmt.Errorf("unresolved control %s must not have a channel ID", c.Login)
			}
			continue
		case "nominated":
			if c.ChannelID <= 0 {
				return nil, fmt.Errorf("nominated control %s needs a verified channel ID", c.Login)
			}
		default:
			return nil, fmt.Errorf("unknown control status %q", c.Status)
		}
		if _, exists := candidates[c.ChannelID]; exists {
			return nil, fmt.Errorf("duplicate nominated channel ID")
		}
		candidates[c.ChannelID] = c
	}
	filtered := []Packet{}
	streams := map[int64]bool{}
	for _, p := range pool {
		if err := p.Verify(); err != nil {
			return nil, err
		}
		if _, ok := candidates[p.Input.Ref.ChannelID]; ok {
			filtered = append(filtered, p)
			streams[p.Input.Ref.ID] = true
		}
	}
	if len(streams) == 0 {
		return nil, fmt.Errorf("no snapshots for resolved nominated channels in the supplied pool")
	}
	cases, err := Sample(filtered, len(streams), 17)
	if err != nil {
		return nil, err
	}
	for i := range cases {
		candidate := candidates[cases[i].Input.Ref.ChannelID]
		cases[i].Challenge = true
		cases[i].Stratum = "owner_nominated"
		cases[i].Nomination = &ControlNomination{Login: candidate.Login, Source: roster.Source, Status: "nominated; stream traffic unreviewed"}
	}
	slices.SortFunc(cases, func(a, b Case) int {
		if a.Input.Ref.ID < b.Input.Ref.ID {
			return -1
		}
		if a.Input.Ref.ID > b.Input.Ref.ID {
			return 1
		}
		return 0
	})
	return cases, nil
}
