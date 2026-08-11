-- +goose Up
-- priority is the key's rank in the gateway's upstream admission queue: when the
-- Ollama pool is saturated, higher-priority requests are admitted first. It
-- mirrors apikey.APIKey.Priority (0 = normal/free .. 9 = the operator's own
-- front-of-house traffic). Existing keys default to 0, which preserves the
-- pre-queue behaviour: everyone competes equally for spare capacity.
ALTER TABLE api_keys ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE api_keys DROP COLUMN priority;
