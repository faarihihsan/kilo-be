-- +goose Up
CREATE TABLE auth_tokens (
    id           uuid        NOT NULL,
    user_id      uuid        NOT NULL,
    -- SHA-256 of the raw token as sent by the client. The raw token is never stored.
    token_hash   bytea       NOT NULL,
    device_name  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at   timestamptz,

    CONSTRAINT auth_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT auth_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT auth_tokens_token_hash_uniq UNIQUE (token_hash),
    CONSTRAINT auth_tokens_device_name_len_chk CHECK (char_length(device_name) <= 100)
);

CREATE INDEX auth_tokens_user_id_idx ON auth_tokens (user_id);

-- +goose Down
DROP TABLE auth_tokens;
