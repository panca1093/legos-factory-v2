-- +goose Up
UPDATE usage_records SET account_id = 'untracked' WHERE account_id = '';

-- +goose Down
UPDATE usage_records SET account_id = '' WHERE account_id = 'untracked';
