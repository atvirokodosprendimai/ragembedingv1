-- +goose Up
-- api_keys holds one row per issued credential. Only the SHA-256 hash of the
-- key is stored (key_hash), never the plaintext. The limit columns mirror
-- apikey.APIKey; token_budget of -1 means unlimited, budget_period is
-- 'monthly' or 'lifetime'. revoked_at is NULL for an active key.
CREATE TABLE api_keys (
    id            INTEGER  PRIMARY KEY AUTOINCREMENT,
    name          TEXT     NOT NULL DEFAULT '',
    key_hash      TEXT     NOT NULL,
    batch_max     INTEGER  NOT NULL,
    rate_per_min  INTEGER  NOT NULL,
    token_budget  INTEGER  NOT NULL,
    budget_period TEXT     NOT NULL,
    revoked_at    DATETIME,
    created_at    DATETIME NOT NULL
);

-- Auth looks a key up by its hash on every request, and a hash must be unique.
CREATE UNIQUE INDEX idx_api_keys_key_hash ON api_keys (key_hash);

-- +goose Down
DROP TABLE api_keys;
