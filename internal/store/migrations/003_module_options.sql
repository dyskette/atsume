-- +goose Up
-- Options are keyed by module name, matching module_credentials. The original
-- table keyed them by the module's hex id, which nothing else uses and which is
-- not what the interface has to hand.
DROP TABLE IF EXISTS module_options;
CREATE TABLE module_options (
    module_name TEXT NOT NULL,
    name        TEXT NOT NULL,
    value       TEXT NOT NULL,
    PRIMARY KEY (module_name, name)
);

-- +goose Down
DROP TABLE module_options;
