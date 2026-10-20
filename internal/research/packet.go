// Package research implements private, offline replay and reviewed evaluation.
package research

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/headcount/internal/score"
)

type Packet struct {
	Input  score.Snapshot `json:"input"`
	Output score.Result   `json:"output"`
	Digest string         `json:"input_sha256"`
}

func Digest(in score.Snapshot) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func (p Packet) Verify() error {
	if p.Input.Schema != 1 {
		return fmt.Errorf("unsupported snapshot schema %d", p.Input.Schema)
	}
	h, err := Digest(p.Input)
	if err != nil {
		return err
	}
	if p.Digest != h {
		return fmt.Errorf("snapshot checksum mismatch")
	}
	r := p.Input.Ref
	if r.ID <= 0 || r.ChannelID <= 0 || p.Input.ObservedAt.IsZero() || r.StartedAt.IsZero() || r.FirstSeenAt.IsZero() || r.LastSeenAt.Before(r.FirstSeenAt) || r.LastSeenAt.Sub(r.FirstSeenAt) > 7*24*time.Hour {
		return fmt.Errorf("invalid snapshot observation bounds")
	}
	return nil
}
func ReadPacket(path string) (Packet, error) {
	var p Packet
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, p.Verify()
}

// WriteNew never replaces a reviewed packet or labels. Exported author summaries
// and annotations are private despite containing no raw message text.
func WriteNew(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
