-- +goose Up
-- Legacy Knowledge Planet accounts contain a private-web Cookie token. Sources,
-- archives, comments, attachments and synchronization cursors are deliberately
-- independent rows and remain untouched.
DELETE FROM platform_accounts WHERE platform = 'zsxq';

-- +goose Down
-- A removed secret cannot and must not be reconstructed.
SELECT 1;
