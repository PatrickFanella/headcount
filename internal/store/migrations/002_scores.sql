-- final = false marks a provisional score for a stream that is still live.
ALTER TABLE stream_scores ADD COLUMN final boolean NOT NULL DEFAULT true;
CREATE INDEX stream_scores_recent_idx ON stream_scores (scored_at DESC);
