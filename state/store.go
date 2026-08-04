package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/linxin2429/bili_notify/model"
	"github.com/linxin2429/bili_notify/vault"
	bolt "go.etcd.io/bbolt"
)

var (
	bucketMeta       = []byte("meta")
	bucketUPs        = []byte("ups")
	bucketChannels   = []byte("channels")
	bucketAuth       = []byte("auth")
	bucketSeen       = []byte("seen")
	bucketDeliveries = []byte("deliveries")
	keySession         = []byte("session")
	keyAdminHash       = []byte("admin_password_hash")
	keyRuntimeSettings = []byte("runtime_settings")
	ErrNotFound        = errors.New("record not found")
	ErrInitialized     = errors.New("administrator is already initialized")
)

type Store struct {
	db    *bolt.DB
	vault *vault.Vault
}

func Open(path string, v *vault.Vault) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("opening state database: %w", err)
	}
	s := &Store{db: db, vault: v}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketMeta, bucketUPs, bucketChannels, bucketAuth, bucketSeen, bucketDeliveries} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("creating bucket %q: %w", name, err)
			}
		}
		meta := tx.Bucket(bucketMeta)
		version := meta.Get([]byte("schema_version"))
		if version == nil {
			return meta.Put([]byte("schema_version"), []byte("2"))
		}
		if string(version) != "2" {
			return fmt.Errorf("unsupported database schema %q", version)
		}
		return nil
	})
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) AdminPasswordHash() (string, error) {
	var hash string
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(bucketMeta).Get(keyAdminHash)
		if value == nil {
			return ErrNotFound
		}
		hash = string(value)
		return nil
	})
	return hash, err
}

func (s *Store) InitializeAdminPasswordHash(hash string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMeta)
		if bucket.Get(keyAdminHash) != nil {
			return ErrInitialized
		}
		return bucket.Put(keyAdminHash, []byte(hash))
	})
}

func (s *Store) SetAdminPasswordHash(hash string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketMeta)
		if bucket.Get(keyAdminHash) == nil {
			return ErrNotFound
		}
		return bucket.Put(keyAdminHash, []byte(hash))
	})
}

func (s *Store) RuntimeSettings() (model.RuntimeSettings, error) {
	var settings model.RuntimeSettings
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketMeta).Get(keyRuntimeSettings)
		if raw == nil {
			return ErrNotFound
		}
		return readJSON(raw, &settings)
	})
	return settings, err
}

func (s *Store) PutRuntimeSettings(settings model.RuntimeSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(bucketMeta), keyRuntimeSettings, settings)
	})
}

func encryptedAAD(bucket, key []byte) []byte {
	aad := make([]byte, 0, len(bucket)+1+len(key))
	aad = append(aad, bucket...)
	aad = append(aad, 0)
	return append(aad, key...)
}

func (s *Store) putEncrypted(b *bolt.Bucket, bucket, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding encrypted record: %w", err)
	}
	sealed, err := s.vault.Seal(raw, encryptedAAD(bucket, key))
	if err != nil {
		return err
	}
	return b.Put(key, sealed)
}

func (s *Store) getEncrypted(b *bolt.Bucket, bucket, key []byte, dst any) error {
	sealed := b.Get(key)
	if sealed == nil {
		return ErrNotFound
	}
	raw, err := s.vault.Open(sealed, encryptedAAD(bucket, key))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decoding encrypted record: %w", err)
	}
	return nil
}

func putJSON(b *bolt.Bucket, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	return b.Put(key, raw)
}

func readJSON(raw []byte, dst any) error {
	if raw == nil {
		return ErrNotFound
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decoding record: %w", err)
	}
	return nil
}

func (s *Store) ListUPs() ([]model.UP, error) {
	ups := make([]model.UP, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketUPs).ForEach(func(_, raw []byte) error {
			var up model.UP
			if err := readJSON(raw, &up); err != nil {
				return err
			}
			ups = append(ups, up)
			return nil
		})
	})
	slices.SortFunc(ups, func(a, b model.UP) int { return a.UIDCompare(b) })
	return ups, err
}

func (s *Store) UP(uid string) (model.UP, error) {
	var up model.UP
	err := s.db.View(func(tx *bolt.Tx) error { return readJSON(tx.Bucket(bucketUPs).Get([]byte(uid)), &up) })
	return up, err
}

func (s *Store) PutUP(up model.UP) error {
	if err := up.Validate(); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketUPs)
		if bucket.Get([]byte(up.UID)) == nil {
			stats := bucket.Stats()
			if stats.KeyN >= 100 {
				return errors.New("at most 100 UPs can be configured")
			}
		}
		return putJSON(bucket, []byte(up.UID), up)
	})
}

func (s *Store) DeleteUP(uid string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bucketUPs).Delete([]byte(uid)); err != nil {
			return err
		}
		err := tx.Bucket(bucketSeen).DeleteBucket([]byte(uid))
		if errors.Is(err, bolt.ErrBucketNotFound) {
			return nil
		}
		return err
	})
}

func (s *Store) SetUPResult(uid, name string, at time.Time, pollErr error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUPs)
		var up model.UP
		if err := readJSON(b.Get([]byte(uid)), &up); err != nil {
			return err
		}
		up.LastPollAt = at
		if name != "" {
			up.Name = name
		}
		if pollErr == nil {
			up.LastSuccessAt = at
			up.LastError = ""
			up.ConsecutiveFail = 0
		} else {
			up.LastError = pollErr.Error()
			up.ConsecutiveFail++
		}
		return putJSON(b, []byte(uid), up)
	})
}

func (s *Store) ListChannels() ([]model.Channel, error) {
	channels := make([]model.Channel, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChannels)
		return b.ForEach(func(k, _ []byte) error {
			var ch model.Channel
			if err := s.getEncrypted(b, bucketChannels, k, &ch); err != nil {
				return err
			}
			channels = append(channels, ch)
			return nil
		})
	})
	slices.SortFunc(channels, func(a, b model.Channel) int { return a.NameCompare(b) })
	return channels, err
}

func (s *Store) Channel(id string) (model.Channel, error) {
	var ch model.Channel
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.getEncrypted(tx.Bucket(bucketChannels), bucketChannels, []byte(id), &ch)
	})
	return ch, err
}

func (s *Store) PutChannel(ch model.Channel) (model.Channel, error) {
	if err := ch.Validate(); err != nil {
		return model.Channel{}, err
	}
	now := time.Now().UTC()
	if ch.ID == "" {
		id, err := randomID()
		if err != nil {
			return model.Channel{}, err
		}
		ch.ID = id
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	err := s.db.Update(func(tx *bolt.Tx) error {
		return s.putEncrypted(tx.Bucket(bucketChannels), bucketChannels, []byte(ch.ID), ch)
	})
	return ch, err
}

func (s *Store) UpdateChannelSettings(id string, settings map[string]string) (model.Channel, error) {
	var channel model.Channel
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketChannels)
		if err := s.getEncrypted(bucket, bucketChannels, []byte(id), &channel); err != nil {
			return err
		}
		if channel.Settings == nil {
			channel.Settings = make(map[string]string)
		}
		for key, value := range settings {
			channel.Settings[key] = value
		}
		channel.UpdatedAt = time.Now().UTC()
		if err := channel.Validate(); err != nil {
			return err
		}
		return s.putEncrypted(bucket, bucketChannels, []byte(id), channel)
	})
	return channel, err
}

func (s *Store) DeleteChannel(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDeliveries)
		if err := b.ForEach(func(_, raw []byte) error {
			var d model.Delivery
			if err := readJSON(raw, &d); err != nil {
				return err
			}
			if d.ChannelID == id {
				return errors.New("channel has pending deliveries")
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket(bucketChannels).Delete([]byte(id))
	})
}

func (s *Store) SaveSession(session model.BiliSession) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return s.putEncrypted(tx.Bucket(bucketAuth), bucketAuth, keySession, session)
	})
}

func (s *Store) Session() (model.BiliSession, error) {
	var session model.BiliSession
	err := s.db.View(func(tx *bolt.Tx) error {
		return s.getEncrypted(tx.Bucket(bucketAuth), bucketAuth, keySession, &session)
	})
	return session, err
}

func (s *Store) ClearSession() error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketAuth).Delete(keySession) })
}

func (s *Store) Seen(uid, dynamicID string) (bool, error) {
	var seen bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSeen).Bucket([]byte(uid))
		seen = b != nil && b.Get([]byte(dynamicID)) != nil
		return nil
	})
	return seen, err
}

// RecordDynamics atomically records unseen dynamics and creates one delivery per enabled channel.
func (s *Store) RecordDynamics(uid string, dynamics []model.Dynamic, channelIDs []string, baseline bool) (int, error) {
	created := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		seenRoot := tx.Bucket(bucketSeen)
		seen, err := seenRoot.CreateBucketIfNotExists([]byte(uid))
		if err != nil {
			return err
		}
		deliveries := tx.Bucket(bucketDeliveries)
		for _, dynamic := range dynamics {
			if seen.Get([]byte(dynamic.ID)) != nil {
				continue
			}
			if err := seen.Put([]byte(dynamic.ID), []byte(dynamic.PublishedAt.UTC().Format(time.RFC3339Nano))); err != nil {
				return err
			}
			if baseline {
				continue
			}
			for _, channelID := range channelIDs {
				d := model.Delivery{
					ID:        dynamic.ID + ":" + channelID,
					Dynamic:   dynamic,
					ChannelID: channelID,
					State:     model.DeliveryPending,
					NextAt:    time.Now().UTC(),
					CreatedAt: time.Now().UTC(),
				}
				if err := putJSON(deliveries, []byte(d.ID), d); err != nil {
					return err
				}
			}
			created++
		}
		if baseline {
			ups := tx.Bucket(bucketUPs)
			var up model.UP
			if err := readJSON(ups.Get([]byte(uid)), &up); err != nil {
				return err
			}
			up.BaselineReady = true
			return putJSON(ups, []byte(uid), up)
		}
		return nil
	})
	return created, err
}

func (s *Store) ListDeliveries(limit int) ([]model.Delivery, error) {
	deliveries := make([]model.Delivery, 0)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDeliveries).ForEach(func(_, raw []byte) error {
			var d model.Delivery
			if err := readJSON(raw, &d); err != nil {
				return err
			}
			deliveries = append(deliveries, d)
			return nil
		})
	})
	slices.SortFunc(deliveries, func(a, b model.Delivery) int { return a.NextAt.Compare(b.NextAt) })
	if limit > 0 && len(deliveries) > limit {
		deliveries = deliveries[:limit]
	}
	return deliveries, err
}

func (s *Store) DueDeliveries(now time.Time, limit int) ([]model.Delivery, error) {
	all, err := s.ListDeliveries(0)
	if err != nil {
		return nil, err
	}
	due := make([]model.Delivery, 0, min(limit, len(all)))
	for _, d := range all {
		if d.State == model.DeliveryPending && !d.NextAt.After(now) {
			due = append(due, d)
			if len(due) == limit {
				break
			}
		}
	}
	return due, nil
}

func (s *Store) CompleteDelivery(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketDeliveries).Delete([]byte(id)) })
}

func (s *Store) FailDelivery(id string, blocked bool, next time.Time, deliveryErr error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDeliveries)
		var d model.Delivery
		if err := readJSON(b.Get([]byte(id)), &d); err != nil {
			return err
		}
		d.Attempts++
		d.NextAt = next.UTC()
		d.LastError = deliveryErr.Error()
		if blocked {
			d.State = model.DeliveryBlocked
		}
		return putJSON(b, []byte(id), d)
	})
}

func (s *Store) UnblockChannel(channelID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDeliveries)
		var updates []model.Delivery
		if err := b.ForEach(func(_, raw []byte) error {
			var d model.Delivery
			if err := readJSON(raw, &d); err != nil {
				return err
			}
			if d.ChannelID == channelID && d.State == model.DeliveryBlocked {
				d.State = model.DeliveryPending
				d.NextAt = time.Now().UTC()
				updates = append(updates, d)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, d := range updates {
			if err := putJSON(b, []byte(d.ID), d); err != nil {
				return err
			}
		}
		return nil
	})
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating channel ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
