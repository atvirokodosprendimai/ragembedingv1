package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/apikey"
)

// APIKeyRepo is the GORM-backed implementation of apikey.Repository. It is the
// concrete adapter; callers hold the interface.
type APIKeyRepo struct {
	db *gorm.DB
}

// NewAPIKeyRepo returns a repository over db.
func NewAPIKeyRepo(db *gorm.DB) *APIKeyRepo { return &APIKeyRepo{db: db} }

// Ensure the interface is satisfied at compile time.
var _ apikey.Repository = (*APIKeyRepo)(nil)

// Create persists a new key.
func (r *APIKeyRepo) Create(ctx context.Context, k *apikey.APIKey) error {
	if err := r.db.WithContext(ctx).Create(k).Error; err != nil {
		return fmt.Errorf("apikey repo: create: %w", err)
	}
	return nil
}

// ByHash returns the key with the given hash. A missing row is translated to the
// domain's apikey.ErrNotFound so the auth layer never sees a GORM-specific error.
func (r *APIKeyRepo) ByHash(ctx context.Context, hash string) (*apikey.APIKey, error) {
	var k apikey.APIKey
	err := r.db.WithContext(ctx).Where("key_hash = ?", hash).First(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apikey.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("apikey repo: by hash: %w", err)
	}
	return &k, nil
}

// ByID returns the key with the given id, translating a missing row to the
// domain's apikey.ErrNotFound.
func (r *APIKeyRepo) ByID(ctx context.Context, id uint) (*apikey.APIKey, error) {
	var k apikey.APIKey
	err := r.db.WithContext(ctx).First(&k, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apikey.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("apikey repo: by id: %w", err)
	}
	return &k, nil
}

// List returns all keys, newest first, for the CLI and dashboard.
func (r *APIKeyRepo) List(ctx context.Context) ([]apikey.APIKey, error) {
	var keys []apikey.APIKey
	if err := r.db.WithContext(ctx).Order("created_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("apikey repo: list: %w", err)
	}
	return keys, nil
}

// UpdateLimits writes a key's governance in one statement. The caller validates
// the limits (apikey.Limits.Validate); this is a plain column write so an
// operator can retune a live key without reissuing it.
//
// Unlike Revoke, an unknown id is an error rather than a no-op: revoking a key
// that is already gone is harmless, but silently discarding a limit change
// leaves an operator believing they raised a cap that never moved.
func (r *APIKeyRepo) UpdateLimits(ctx context.Context, id uint, l apikey.Limits) error {
	res := r.db.WithContext(ctx).
		Model(&apikey.APIKey{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"batch_max":     l.BatchMax,
			"rate_per_min":  l.RatePerMin,
			"token_budget":  l.TokenBudget,
			"budget_period": l.BudgetPeriod,
			"priority":      l.Priority,
		})
	if res.Error != nil {
		return fmt.Errorf("apikey repo: update limits: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return apikey.ErrNotFound
	}
	return nil
}

// Revoke stamps revoked_at on the key so subsequent auth attempts fail. Revoking
// an already-revoked or unknown id is a no-op rather than an error, keeping the
// operation idempotent.
func (r *APIKeyRepo) Revoke(ctx context.Context, id uint) error {
	now := time.Now()
	err := r.db.WithContext(ctx).
		Model(&apikey.APIKey{}).
		Where("id = ?", id).
		Update("revoked_at", now).Error
	if err != nil {
		return fmt.Errorf("apikey repo: revoke: %w", err)
	}
	return nil
}
