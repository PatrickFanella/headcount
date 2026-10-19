// Package score turns finished streams into suspicion scores.
package score

import (
	"context"
	"log/slog"

	"git.subcult.tv/subculture-collective/sus-quotient/internal/store"
)

type Scorer struct {
	store *store.Store
	log   *slog.Logger
}

func New(s *store.Store, log *slog.Logger) *Scorer { return &Scorer{store: s, log: log} }

func (s *Scorer) Run(ctx context.Context) { <-ctx.Done() }
