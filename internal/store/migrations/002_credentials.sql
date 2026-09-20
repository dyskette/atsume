-- +goose Up
-- Credentials for the modules that implement OnLogin. The password is stored
-- encrypted; see internal/store/secret.go.
CREATE TABLE module_credentials (
    module_name  TEXT PRIMARY KEY,
    username     TEXT NOT NULL,
    password_enc BLOB NOT NULL,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE module_credentials;
