-- Start times of viewer spikes, written by the scorer, used to find channels
-- whose spikes begin in the same minutes.
CREATE TABLE spike_onsets (
    stream_id  bigint NOT NULL,
    channel_id bigint NOT NULL,
    ts         timestamptz NOT NULL,
    rel        real NOT NULL,
    PRIMARY KEY (stream_id, ts)
);
CREATE INDEX spike_onsets_ts_idx ON spike_onsets (ts);
