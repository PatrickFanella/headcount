-- Shared-account lookups read a chatter's other channels in a time window;
-- category names are looked up by id.
CREATE INDEX stream_chatters_user_seen_idx ON stream_chatters (user_id, last_seen_at) INCLUDE (channel_id);
DROP INDEX stream_chatters_user_idx;
CREATE INDEX streams_game_idx ON streams (game_id, last_seen_at DESC) WHERE game_name IS NOT NULL;
