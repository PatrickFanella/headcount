-- Additive migration. Legacy rows remain unknown, never presumed healthy.
ALTER TABLE chat_minutes ADD COLUMN healthy boolean;
ALTER TABLE chat_minutes ADD COLUMN commands int NOT NULL DEFAULT 0;
ALTER TABLE chat_minutes ADD COLUMN utility_messages int NOT NULL DEFAULT 0;
ALTER TABLE chat_minutes ADD COLUMN bot_badge_messages int NOT NULL DEFAULT 0;
ALTER TABLE chat_minutes ADD COLUMN duplicate_messages int NOT NULL DEFAULT 0;
ALTER TABLE chat_minutes ADD COLUMN delayed_messages int NOT NULL DEFAULT 0;
ALTER TABLE chat_minutes ADD COLUMN source_delay_ms bigint NOT NULL DEFAULT 0;

ALTER TABLE stream_chatters ADD COLUMN active_minutes int NOT NULL DEFAULT 0;
ALTER TABLE stream_chatters ADD COLUMN commands int NOT NULL DEFAULT 0;
ALTER TABLE stream_chatters ADD COLUMN utility_messages int NOT NULL DEFAULT 0;
ALTER TABLE stream_chatters ADD COLUMN bot_badge_messages int NOT NULL DEFAULT 0;
ALTER TABLE stream_chatters ADD COLUMN gap_bins bigint[] NOT NULL DEFAULT '{0,0,0,0}';

-- Current availability is distinct from an immutable creation date.
ALTER TABLE twitch_users ADD COLUMN omission_count int NOT NULL DEFAULT 0;
ALTER TABLE twitch_users ADD COLUMN next_check_at timestamptz NOT NULL DEFAULT now();
UPDATE twitch_users SET omission_count = CASE WHEN missing THEN 1 ELSE 0 END,
 next_check_at = fetched_at + CASE WHEN missing THEN interval '1 hour' ELSE interval '7 days' END;
CREATE INDEX twitch_users_due_idx ON twitch_users (next_check_at);
CREATE TABLE user_observations (
 user_id bigint NOT NULL,
 checked_at timestamptz NOT NULL,
 available boolean NOT NULL,
 created_at timestamptz,
 PRIMARY KEY (user_id, checked_at)
);
INSERT INTO user_observations SELECT id, fetched_at, NOT missing, created_at FROM twitch_users;

ALTER TABLE stream_chatter_summary ADD COLUMN resolved int;
ALTER TABLE stream_chatter_summary ADD COLUMN pending int;
-- Old summaries counted only cached accounts. Their true denominator is unknown.
ALTER TABLE stream_chatter_summary ADD COLUMN coverage_known boolean NOT NULL DEFAULT false;

-- Snapshots are private replay inputs. The public API role must not expose
-- author-level data through a future generic endpoint.
CREATE TABLE score_snapshots (
 stream_id bigint NOT NULL,
 observed_at timestamptz NOT NULL,
 version int NOT NULL,
 input jsonb NOT NULL,
 output jsonb NOT NULL,
 PRIMARY KEY (stream_id, observed_at, version)
);
REVOKE ALL ON score_snapshots, user_observations FROM susq_api;
ALTER TABLE stream_scores ADD COLUMN evidence jsonb NOT NULL DEFAULT '{}';
ALTER TABLE spike_onsets ADD COLUMN version int NOT NULL DEFAULT 0;
-- The same raw, healthy paired-minute metric feeds target and peer scores.
-- Persisting it avoids reaggregating a week of chat for every target stream.
CREATE TABLE stream_peer_metrics (
 stream_id bigint PRIMARY KEY REFERENCES streams(id),
 metric_version int NOT NULL,
 source_last_seen timestamptz NOT NULL,
 language text NOT NULL,
 game_id text NOT NULL,
 game_name text NOT NULL,
 median_viewers double precision NOT NULL,
 chatters_per_k double precision NOT NULL
);
CREATE TABLE stream_behavior_metrics (
 stream_id bigint PRIMARY KEY REFERENCES streams(id),
 source_last_seen timestamptz NOT NULL,
 metrics jsonb NOT NULL
);
CREATE INDEX score_snapshots_retention_idx ON score_snapshots(observed_at);
CREATE INDEX user_observations_retention_idx ON user_observations(checked_at);
