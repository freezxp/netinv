package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/freezxp/netinv/backend/internal/platform/errx"
)

// AppearanceKey is the settings row holding the UI's deployment colours.
const AppearanceKey = "ui.appearance"

// Appearance is how the web UI is coloured for this deployment (doc 30 §0).
//
// It is deployment configuration, like report branding, because an operator
// who runs NetInv beside other internal tools wants it to carry the same
// organisation colour as they do — and that colour belongs to the deployment,
// not to the public repository, which keeps a neutral default.
type Appearance struct {
	// Accent is the primary colour as #rrggbb; empty means the default. The UI
	// derives every accent shade, light and dark, from this one value.
	Accent string `json:"accent"`
}

func (s *Store) GetAppearance(ctx context.Context) (Appearance, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx,
		`SELECT value FROM config.settings WHERE key = $1`, AppearanceKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Appearance{}, nil
	}
	if err != nil {
		return Appearance{}, errx.Wrap(errx.KindTransient, err, "read appearance setting")
	}
	var a Appearance
	if err := json.Unmarshal(raw, &a); err != nil {
		return Appearance{}, errx.Wrap(errx.KindInternal, err, "decode appearance setting")
	}
	return a, nil
}

func (s *Store) PutAppearance(ctx context.Context, a Appearance, actor string) (Appearance, error) {
	a, err := a.normalized()
	if err != nil {
		return Appearance{}, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return Appearance{}, errx.Wrap(errx.KindInternal, err, "encode appearance setting")
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO config.settings (key, tenant_id, value, value_schema, updated_by, updated_at)
		VALUES ($1, 't_default', $2, 'netinv.ui.appearance/1', nullif($3,''), now())
		ON CONFLICT (key) DO UPDATE SET
			value = excluded.value, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`, AppearanceKey, raw, actor); err != nil {
		return Appearance{}, errx.Wrap(errx.KindTransient, err, "write appearance setting")
	}
	return a, nil
}

func (a Appearance) normalized() (Appearance, error) {
	a.Accent = strings.ToLower(strings.TrimSpace(a.Accent))
	if a.Accent != "" && !hexColour.MatchString(a.Accent) {
		return Appearance{}, errx.New(errx.KindInvalid,
			"accent must be a #rrggbb colour, got %q", a.Accent)
	}
	return a, nil
}
