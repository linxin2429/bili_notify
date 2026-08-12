package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/linxin2429/bili_notify/vault"
)

// migrateV11 invalidates credentials whose consent or protocol changed. This
// is deliberately irreversible: retaining an apparently authorized channel
// would make deliveries fail later with misleading protocol errors.
func migrateV11(v *vault.Vault) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,type,enabled,public_settings_json,secret_sealed FROM channels WHERE type IN ('microsoft','feishu') ORDER BY id`)
		if err != nil {
			return err
		}
		type record struct {
			id, kind, public string
			enabled          int
			sealed           []byte
		}
		var records []record
		for rows.Next() {
			var item record
			if err := rows.Scan(&item.id, &item.kind, &item.enabled, &item.public, &item.sealed); err != nil {
				_ = rows.Close()
				return err
			}
			records = append(records, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range records {
			settings, secrets := map[string]string{}, map[string]string{}
			if err := json.Unmarshal([]byte(item.public), &settings); err != nil {
				return fmt.Errorf("decoding channel %s settings: %w", item.id, err)
			}
			if err := openJSON(v, tableChannelSecrets, item.id, item.sealed, &secrets); err != nil {
				return fmt.Errorf("opening channel %s secrets: %w", item.id, err)
			}
			enabled := item.enabled
			if item.kind == "microsoft" {
				enabled = 0
				for _, key := range []string{"authorized", "token_type", "token_expiry"} {
					delete(settings, key)
				}
				delete(secrets, "access_token")
				delete(secrets, "refresh_token")
			} else {
				delete(secrets, "webhook")
				delete(secrets, "secret")
				if strings.TrimSpace(settings["chat_id"]) == "" {
					enabled = 0
				}
			}
			publicJSON, err := json.Marshal(settings)
			if err != nil {
				return err
			}
			sealed, err := sealJSON(v, tableChannelSecrets, item.id, secrets)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE channels SET enabled=?,public_settings_json=?,secret_sealed=? WHERE id=?`, enabled, string(publicJSON), sealed, item.id); err != nil {
				return err
			}
		}
		return nil
	}
}
