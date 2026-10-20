package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// RecordDistributionContext stores sourced, bounded context. It is an explicit
// operator annotation, never an inference from a quiet chat or a count jump.
func (s *Store) RecordDistributionContext(ctx context.Context, streamID int64, from, until time.Time, kind, source string) error {
	if !slices.Contains([]string{"embed", "promotion", "drops", "ads", "collaboration"}, kind) || source == "" || !until.After(from) {
		return fmt.Errorf("context needs a known kind, source and increasing bounds")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var channel int64
	var start, end time.Time
	if err := tx.QueryRow(ctx, `SELECT channel_id,started_at,last_seen_at FROM streams WHERE id=$1 FOR UPDATE`, streamID).Scan(&channel, &start, &end); err != nil {
		return err
	}
	if from.Before(start) || until.After(end.Add(time.Minute)) {
		return fmt.Errorf("context must lie within observed stream bounds")
	}
	detail, err := json.Marshal(map[string]any{"category": kind, "source": source, "until": until.UTC().Format(time.RFC3339), "recorded_at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO channel_events(ts,channel_id,stream_id,kind,detail) VALUES($1,$2,$3,'distribution_context',$4)`, from, channel, streamID, detail); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM stream_peer_metrics WHERE stream_id=$1`, streamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE streams SET scored_at=NULL WHERE id=$1`, streamID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
