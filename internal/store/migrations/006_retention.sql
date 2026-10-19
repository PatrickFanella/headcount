-- Per-stream chatter account summary, written just before a stream's
-- stream_chatters rows are pruned so rescoring and charts still work.
CREATE TABLE stream_chatter_summary (
    stream_id      bigint PRIMARY KEY REFERENCES streams (id),
    total          int NOT NULL,
    new_accounts   int NOT NULL,  -- created within 30 days before the stream started
    missing        int NOT NULL,
    created_months jsonb NOT NULL DEFAULT '{}',  -- {"2024-03": n} by account creation month
    summarized_at  timestamptz NOT NULL
);

CREATE INDEX twitch_users_fetched_idx ON twitch_users (fetched_at);

-- Compress sooner; compressed chunks are still read for rescoring.
SELECT remove_compression_policy('viewer_samples');
SELECT remove_compression_policy('follower_samples');
SELECT remove_compression_policy('chat_minutes');
SELECT add_compression_policy('viewer_samples', INTERVAL '7 days');
SELECT add_compression_policy('follower_samples', INTERVAL '7 days');
SELECT add_compression_policy('chat_minutes', INTERVAL '7 days');
