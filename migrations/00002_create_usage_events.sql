-- +goose Up
-- usage_events records one row per successful embedding call. prompt_tokens is
-- the authoritative bge-m3 count taken from Ollama's usage.prompt_tokens, so
-- every row is a real, billed measurement. batch_size is the number of inputs
-- in that request (for context on the dashboard).
CREATE TABLE usage_events (
    id            INTEGER  PRIMARY KEY AUTOINCREMENT,
    api_key_id    INTEGER  NOT NULL,
    model         TEXT     NOT NULL,
    prompt_tokens INTEGER  NOT NULL,
    batch_size    INTEGER  NOT NULL,
    created_at    DATETIME NOT NULL
);

-- The dashboard sums tokens per key within time ranges; a composite index on
-- (api_key_id, created_at) serves both the key filter and the range scan.
CREATE INDEX idx_usage_key_time ON usage_events (api_key_id, created_at);

-- +goose Down
DROP TABLE usage_events;
