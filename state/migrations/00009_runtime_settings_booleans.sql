-- +goose Up
UPDATE meta
SET value = json_set(
  value,
  '$.bilibili_comments_enabled',
  json(CASE json_extract(value, '$.bilibili_comments_enabled')
    WHEN 0 THEN 'false'
    WHEN 1 THEN 'true'
  END)
)
WHERE key = 'runtime_settings'
  AND json_type(value, '$.bilibili_comments_enabled') = 'integer'
  AND json_extract(value, '$.bilibili_comments_enabled') IN (0, 1);

-- +goose Down
UPDATE meta
SET value = json_set(
  value,
  '$.bilibili_comments_enabled',
  CASE json_type(value, '$.bilibili_comments_enabled')
    WHEN 'true' THEN 1
    WHEN 'false' THEN 0
  END
)
WHERE key = 'runtime_settings'
  AND json_type(value, '$.bilibili_comments_enabled') IN ('true', 'false');
