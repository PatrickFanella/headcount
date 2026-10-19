CREATE EXTENSION IF NOT EXISTS timescaledb;

-- Every channel that has appeared in the global top list. Channels are never
-- deleted; low-scoring channels move to the cold tier and are polled less often.
CREATE TABLE channels (
    id                  bigint PRIMARY KEY,
    login               text NOT NULL,
    display_name        text NOT NULL,
    tier                text NOT NULL DEFAULT 'hot' CHECK (tier IN ('hot', 'tracked', 'cold')),
    first_seen_at       timestamptz NOT NULL,
    last_top_at         timestamptz,
    best_rank           int,
    last_live_at        timestamptz,
    quotient            real,
    quotient_confidence real,
    quotient_updated_at timestamptz
);
CREATE INDEX channels_tier_idx ON channels (tier);

CREATE TABLE streams (
    id           bigint PRIMARY KEY,
    channel_id   bigint NOT NULL REFERENCES channels (id),
    started_at   timestamptz NOT NULL,
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    ended_at     timestamptz,
    title        text,
    game_id      text,
    game_name    text,
    language     text,
    peak_viewers int NOT NULL DEFAULT 0,
    scored_at    timestamptz
);
CREATE INDEX streams_channel_idx ON streams (channel_id, started_at DESC);
CREATE INDEX streams_unscored_idx ON streams (ended_at) WHERE scored_at IS NULL AND ended_at IS NOT NULL;
CREATE INDEX streams_open_idx ON streams (channel_id) WHERE ended_at IS NULL;

-- One row per channel per minute while live. rank is the position in the
-- global top list when the channel was in it.
CREATE TABLE viewer_samples (
    ts         timestamptz NOT NULL,
    channel_id bigint NOT NULL,
    stream_id  bigint NOT NULL,
    viewers    int NOT NULL,
    game_id    text,
    rank       smallint,
    PRIMARY KEY (channel_id, ts)
);
SELECT create_hypertable('viewer_samples', by_range('ts', INTERVAL '7 days'));

CREATE TABLE follower_samples (
    ts         timestamptz NOT NULL,
    channel_id bigint NOT NULL,
    total      bigint NOT NULL,
    PRIMARY KEY (channel_id, ts)
);
SELECT create_hypertable('follower_samples', by_range('ts', INTERVAL '7 days'));

-- Per-minute chat aggregates. Raw message text is not stored.
CREATE TABLE chat_minutes (
    ts              timestamptz NOT NULL,
    channel_id      bigint NOT NULL,
    stream_id       bigint,
    messages        int NOT NULL,
    unique_chatters int NOT NULL,
    new_chatters    int NOT NULL,  -- first message seen from this user during the stream
    first_msgs      int NOT NULL,  -- Twitch first-msg tag: first ever message in the channel
    emote_only      int NOT NULL,
    distinct_texts  int NOT NULL,
    top_text_count  int NOT NULL,  -- occurrences of the most repeated normalized message
    bans            int NOT NULL,
    timeouts        int NOT NULL,
    PRIMARY KEY (channel_id, ts)
);
SELECT create_hypertable('chat_minutes', by_range('ts', INTERVAL '7 days'));

CREATE TABLE stream_chatters (
    stream_id     bigint NOT NULL,
    user_id       bigint NOT NULL,
    channel_id    bigint NOT NULL,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    messages      int NOT NULL,
    PRIMARY KEY (stream_id, user_id)
);
CREATE INDEX stream_chatters_user_idx ON stream_chatters (user_id);

-- Account creation dates for chatters. missing = Helix returned nothing
-- (suspended or deleted account).
CREATE TABLE twitch_users (
    id         bigint PRIMARY KEY,
    login      text,
    created_at timestamptz,
    missing    boolean NOT NULL DEFAULT false,
    fetched_at timestamptz NOT NULL
);

-- Things that explain viewer changes: raids, title and game changes, chat
-- mode changes, stream start and end.
CREATE TABLE channel_events (
    ts         timestamptz NOT NULL,
    channel_id bigint NOT NULL,
    stream_id  bigint,
    kind       text NOT NULL,
    detail     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX channel_events_idx ON channel_events (channel_id, ts);

CREATE TABLE stream_scores (
    stream_id  bigint PRIMARY KEY REFERENCES streams (id),
    channel_id bigint NOT NULL,
    scored_at  timestamptz NOT NULL,
    version    int NOT NULL,
    minutes    int NOT NULL,
    quotient   real NOT NULL,
    confidence real NOT NULL,
    components jsonb NOT NULL
);
CREATE INDEX stream_scores_channel_idx ON stream_scores (channel_id, scored_at DESC);

ALTER TABLE viewer_samples SET (timescaledb.compress, timescaledb.compress_segmentby = 'channel_id');
ALTER TABLE follower_samples SET (timescaledb.compress, timescaledb.compress_segmentby = 'channel_id');
ALTER TABLE chat_minutes SET (timescaledb.compress, timescaledb.compress_segmentby = 'channel_id');
SELECT add_compression_policy('viewer_samples', INTERVAL '14 days');
SELECT add_compression_policy('follower_samples', INTERVAL '14 days');
SELECT add_compression_policy('chat_minutes', INTERVAL '14 days');
