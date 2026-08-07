// Package postgres — Inventory repositories: sites + envelope-encrypted
// credential vault (ADR-011, doc 20 §7).
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freezxp/netinv/backend/internal/inventory/domain"
	"github.com/freezxp/netinv/backend/internal/platform/cryptox"
	"github.com/freezxp/netinv/backend/internal/platform/errx"
	"github.com/freezxp/netinv/backend/internal/platform/id"
)

type SiteRepo struct{ Pool *pgxpool.Pool }

const siteCols = `id, tenant_id, name, parent_site_id, coalesce(location,''),
	coalesce(contact,''), status, created_at, updated_at`

func scanSite(row pgx.Row) (*domain.Site, error) {
	s := &domain.Site{}
	err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.ParentSiteID, &s.Location,
		&s.Contact, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "site not found")
	}
	return s, errx.Wrap(errx.KindTransient, err, "scan site")
}

func (r *SiteRepo) List(ctx context.Context) ([]*domain.Site, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT `+siteCols+` FROM platform.sites ORDER BY name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list sites")
	}
	defer rows.Close()
	var out []*domain.Site
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *SiteRepo) Get(ctx context.Context, sid string) (*domain.Site, error) {
	return scanSite(r.Pool.QueryRow(ctx,
		`SELECT `+siteCols+` FROM platform.sites WHERE id = $1`, sid))
}

func (r *SiteRepo) Create(ctx context.Context, s *domain.Site) error {
	_, err := r.Pool.Exec(ctx, `
		INSERT INTO platform.sites (id, tenant_id, name, parent_site_id, location, contact, status)
		VALUES ($1,$2,$3,$4,nullif($5,''),nullif($6,''),$7)`,
		s.ID, s.TenantID, s.Name, s.ParentSiteID, s.Location, s.Contact, s.Status)
	if isUnique(err) {
		return errx.New(errx.KindConflict, "a site with that name already exists")
	}
	return errx.Wrap(errx.KindTransient, err, "insert site")
}

func (r *SiteRepo) Update(ctx context.Context, s *domain.Site) error {
	tag, err := r.Pool.Exec(ctx, `
		UPDATE platform.sites SET name=$2, parent_site_id=$3,
			location=nullif($4,''), contact=nullif($5,''), updated_at=now()
		WHERE id=$1`,
		s.ID, s.Name, s.ParentSiteID, s.Location, s.Contact)
	if isUnique(err) {
		return errx.New(errx.KindConflict, "a site with that name already exists")
	}
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "update site")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "site not found")
	}
	return nil
}

func (r *SiteRepo) Delete(ctx context.Context, sid string) error {
	var devices int
	if err := r.Pool.QueryRow(ctx,
		`SELECT count(*) FROM inventory.devices WHERE site_id = $1 AND status != 'retired'`,
		sid).Scan(&devices); err != nil {
		return errx.Wrap(errx.KindTransient, err, "count site devices")
	}
	if devices > 0 {
		return errx.New(errx.KindConflict, "site has managed devices")
	}
	tag, err := r.Pool.Exec(ctx, `DELETE FROM platform.sites WHERE id = $1`, sid)
	if err != nil {
		if isFK(err) {
			return errx.New(errx.KindConflict, "site is referenced (pollers or child sites)")
		}
		return errx.Wrap(errx.KindTransient, err, "delete site")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "site not found")
	}
	return nil
}

// EnvelopeVault implements app.CredentialVault with cryptox envelopes.
type EnvelopeVault struct {
	Pool *pgxpool.Pool
	Keys cryptox.KeyProvider
}

const credCols = `c.id, c.tenant_id, c.name, c.kind, c.meta, c.created_at, c.updated_at,
	(SELECT count(*) FROM inventory.devices d
	  WHERE d.credential_id = c.id AND d.status != 'retired')`

func scanCred(row pgx.Row) (*domain.Credential, error) {
	c := &domain.Credential{}
	var kind string
	err := row.Scan(&c.ID, &c.TenantID, &c.Name, &kind, &c.Meta,
		&c.CreatedAt, &c.UpdatedAt, &c.DeviceCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errx.New(errx.KindNotFound, "credential not found")
	}
	c.Kind = domain.CredentialKind(kind)
	return c, errx.Wrap(errx.KindTransient, err, "scan credential")
}

func (v *EnvelopeVault) List(ctx context.Context) ([]*domain.Credential, error) {
	rows, err := v.Pool.Query(ctx,
		`SELECT `+credCols+` FROM inventory.credentials c ORDER BY c.name`)
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "list credentials")
	}
	defer rows.Close()
	var out []*domain.Credential
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (v *EnvelopeVault) Get(ctx context.Context, cid string) (*domain.Credential, error) {
	return scanCred(v.Pool.QueryRow(ctx,
		`SELECT `+credCols+` FROM inventory.credentials c WHERE c.id = $1`, cid))
}

func (v *EnvelopeVault) seal(secret domain.Secret) (*cryptox.Envelope, error) {
	plain, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return cryptox.Encrypt(v.Keys, plain)
}

func (v *EnvelopeVault) Store(ctx context.Context, name string,
	kind domain.CredentialKind, secret domain.Secret, createdBy string) (*domain.Credential, error) {
	env, err := v.seal(secret)
	if err != nil {
		return nil, errx.Wrap(errx.KindInternal, err, "seal credential")
	}
	cid := id.New("cr")
	meta, _ := json.Marshal(secret.PublicMeta(kind))
	_, err = v.Pool.Exec(ctx, `
		INSERT INTO inventory.credentials
			(id, tenant_id, name, kind, enc_payload, enc_dek, key_version, meta, created_by)
		VALUES ($1,'t_default',$2,$3,$4,$5,$6,$7,nullif($8,''))`,
		cid, name, string(kind), env.Ciphertext, env.WrappedDEK, env.KeyVersion, meta, createdBy)
	if isUnique(err) {
		return nil, errx.New(errx.KindConflict, "a credential with that name already exists")
	}
	if err != nil {
		return nil, errx.Wrap(errx.KindTransient, err, "insert credential")
	}
	return v.Get(ctx, cid)
}

func (v *EnvelopeVault) UpdateSecret(ctx context.Context, cid string, secret domain.Secret) error {
	cred, err := v.Get(ctx, cid)
	if err != nil {
		return err
	}
	env, err := v.seal(secret)
	if err != nil {
		return errx.Wrap(errx.KindInternal, err, "seal credential")
	}
	meta, _ := json.Marshal(secret.PublicMeta(cred.Kind))
	_, err = v.Pool.Exec(ctx, `
		UPDATE inventory.credentials
		SET enc_payload=$2, enc_dek=$3, key_version=$4, meta=$5, updated_at=now()
		WHERE id=$1`,
		cid, env.Ciphertext, env.WrappedDEK, env.KeyVersion, meta)
	return errx.Wrap(errx.KindTransient, err, "update credential")
}

func (v *EnvelopeVault) Rename(ctx context.Context, cid, name string) error {
	tag, err := v.Pool.Exec(ctx,
		`UPDATE inventory.credentials SET name=$2, updated_at=now() WHERE id=$1`, cid, name)
	if isUnique(err) {
		return errx.New(errx.KindConflict, "a credential with that name already exists")
	}
	if err != nil {
		return errx.Wrap(errx.KindTransient, err, "rename credential")
	}
	if tag.RowsAffected() == 0 {
		return errx.New(errx.KindNotFound, "credential not found")
	}
	return nil
}

func (v *EnvelopeVault) Delete(ctx context.Context, cid string) error {
	cred, err := v.Get(ctx, cid)
	if err != nil {
		return err
	}
	if cred.DeviceCount > 0 {
		return errx.New(errx.KindConflict, "credential is in use by devices")
	}
	_, err = v.Pool.Exec(ctx, `DELETE FROM inventory.credentials WHERE id=$1`, cid)
	if isFK(err) {
		return errx.New(errx.KindConflict, "credential is referenced")
	}
	return errx.Wrap(errx.KindTransient, err, "delete credential")
}

func (v *EnvelopeVault) Decrypt(ctx context.Context, cid string) (domain.Secret, error) {
	var env cryptox.Envelope
	err := v.Pool.QueryRow(ctx, `
		SELECT enc_payload, enc_dek, key_version
		FROM inventory.credentials WHERE id = $1`, cid).
		Scan(&env.Ciphertext, &env.WrappedDEK, &env.KeyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Secret{}, errx.New(errx.KindNotFound, "credential not found")
	}
	if err != nil {
		return domain.Secret{}, errx.Wrap(errx.KindTransient, err, "load credential")
	}
	plain, err := cryptox.Decrypt(v.Keys, &env)
	if err != nil {
		return domain.Secret{}, errx.Wrap(errx.KindInternal, err, "unseal credential")
	}
	var s domain.Secret
	if err := json.Unmarshal(plain, &s); err != nil {
		return domain.Secret{}, errx.Wrap(errx.KindInternal, err, "decode credential")
	}
	return s, nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return err != nil && errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isFK(err error) bool {
	var pgErr *pgconn.PgError
	return err != nil && errors.As(err, &pgErr) && pgErr.Code == "23503"
}
