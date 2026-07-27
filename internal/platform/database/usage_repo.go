package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/atvirokodosprendimai/ragembedingv1/internal/domain/usage"
)

// UsageRepo is the GORM-backed implementation of usage.Repository.
type UsageRepo struct {
	db *gorm.DB
}

// NewUsageRepo returns a repository over db.
func NewUsageRepo(db *gorm.DB) *UsageRepo { return &UsageRepo{db: db} }

// Ensure the interface is satisfied at compile time.
var _ usage.Repository = (*UsageRepo)(nil)

// Record persists one usage event.
func (r *UsageRepo) Record(ctx context.Context, e *usage.Event) error {
	if err := r.db.WithContext(ctx).Create(e).Error; err != nil {
		return fmt.Errorf("usage repo: record: %w", err)
	}
	return nil
}

// SumTokens totals prompt_tokens for a key within the half-open range
// [from, to). COALESCE turns the empty-range NULL into 0 so callers always get a
// concrete count; sql.NullInt64 receives the scalar aggregate.
func (r *UsageRepo) SumTokens(ctx context.Context, apiKeyID uint, from, to time.Time) (int64, error) {
	var total sql.NullInt64
	err := r.db.WithContext(ctx).
		Model(&usage.Event{}).
		Where("api_key_id = ? AND created_at >= ? AND created_at < ?", apiKeyID, from, to).
		Select("COALESCE(SUM(prompt_tokens), 0)").
		Scan(&total).Error
	if err != nil {
		return 0, fmt.Errorf("usage repo: sum tokens: %w", err)
	}
	return total.Int64, nil
}
