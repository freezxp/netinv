// Package postgres — notification channels (envelope-encrypted secrets) and
// delivery log.
package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/notify/app"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type ChannelRepo struct {
	Pool *pgxpool.Pool
	Keys cryptox.KeyProvider
}

func (r *ChannelRepo) EnabledChannels(ctx context.Context) ([]app.Channel, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, name, kind, config, enc_secret, enc_dek, coalesce(key_version,1)
		FROM notify.channels WHERE enabled ORDER BY name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "channels")
	}
	defer rows.Close()
	out := []app.Channel{}
	for rows.Next() {
		var ch app.Channel
		var kind string
		var config, encSecret, encDek []byte
		var keyVer int
		if err := rows.Scan(&ch.ID, &ch.Name, &kind, &config, &encSecret,
			&encDek, &keyVer); err != nil {
			return nil, err
		}
		ch.Kind = kind
		_ = json.Unmarshal(config, &ch.Config)
		ch.Secret = map[string]string{}
		if len(encSecret) > 0 {
			plain, err := cryptox.Decrypt(r.Keys, &cryptox.Envelope{
				Ciphertext: encSecret, WrappedDEK: encDek, KeyVersion: keyVer,
			})
			if err != nil {
				return nil, errx.Wrap(errx.KindInternal, err, "unseal channel secret")
			}
			_ = json.Unmarshal(plain, &ch.Secret)
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// Create stores a channel; secret fields are write-only (FR-NOT-05).
func (r *ChannelRepo) Create(ctx context.Context, name, kind string,
	config map[string]any, secret map[string]string) (string, error) {
	cid := id.New("nc")
	cfg, _ := json.Marshal(config)
	var encSecret, encDek []byte
	keyVer := 0
	if len(secret) > 0 {
		plain, _ := json.Marshal(secret)
		env, err := cryptox.Encrypt(r.Keys, plain)
		if err != nil {
			return "", errx.Wrap(errx.KindInternal, err, "seal channel secret")
		}
		encSecret, encDek, keyVer = env.Ciphertext, env.WrappedDEK, env.KeyVersion
	}
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO notify.channels
			(id, tenant_id, name, kind, config, enc_secret, enc_dek, key_version)
		VALUES ($1,'t_default',$2,$3,$4,$5,$6,nullif($7,0))`,
		cid, name, kind, cfg, encSecret, encDek, keyVer)
	if err != nil {
		return "", errx.Wrap(errx.KindConflict, err, "insert channel")
	}
	return cid, nil
}

func (r *ChannelRepo) List(ctx context.Context) ([]map[string]any, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, name, kind, config, enabled, created_at
		FROM notify.channels ORDER BY name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list channels")
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, kind string
		var config []byte
		var enabled bool
		var created time.Time
		if err := rows.Scan(&id, &name, &kind, &config, &enabled, &created); err != nil {
			return nil, err
		}
		var cfg map[string]any
		_ = json.Unmarshal(config, &cfg)
		out = append(out, map[string]any{
			"id": id, "name": name, "kind": kind, "config": cfg,
			"enabled": enabled, "created_at": created.UTC().Format(time.RFC3339),
		})
	}
	return out, rows.Err()
}

func (r *ChannelRepo) Delete(ctx context.Context, cid string) error {
	tag, err := r.Pool.Exec(ctx, `DELETE FROM notify.channels WHERE id=$1`, cid)
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "delete channel")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "channel not found")
	}
	return nil
}

// DeliveryRepo records outcomes (FR-NOT-04).
type DeliveryRepo struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

func (r *DeliveryRepo) Record(ctx context.Context, alertID, channelID, event,
	status string, attempts int, lastErr string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var deliveredAt *time.Time
	if status == "ok" {
		t := time.Now().UTC()
		deliveredAt = &t
	}
	if _, err := r.Pool.Exec(wctx, `
		INSERT INTO notify.deliveries
			(alert_id, channel_id, event, status, attempts, last_error, delivered_at)
		VALUES ($1,$2,$3,$4,$5,nullif($6,''),$7)`,
		alertID, channelID, event, status, attempts, lastErr, deliveredAt); err != nil {
		r.Log.Error("delivery record failed", "err", err)
	}
}
