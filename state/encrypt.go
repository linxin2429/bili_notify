package state

import (
	"encoding/json"
	"fmt"

	"github.com/linxin2429/bili_notify/vault"
)

// sealedAAD binds ciphertext to a SQL table and primary key.
// Format: table || 0x00 || pk (replaces bbolt bucket||0x00||key).
func sealedAAD(table, pk string) []byte {
	aad := make([]byte, 0, len(table)+1+len(pk))
	aad = append(aad, table...)
	aad = append(aad, 0)
	return append(aad, pk...)
}

func sealJSON(v *vault.Vault, table, pk string, value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding sealed record: %w", err)
	}
	sealed, err := v.Seal(raw, sealedAAD(table, pk))
	if err != nil {
		return nil, err
	}
	return sealed, nil
}

func openJSON(v *vault.Vault, table, pk string, sealed []byte, dst any) error {
	raw, err := v.Open(sealed, sealedAAD(table, pk))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decoding sealed record: %w", err)
	}
	return nil
}
