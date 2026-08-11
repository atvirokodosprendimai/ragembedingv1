package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"time"
)

// ErrNotFound is returned by Repository.ByHash when no key matches. It is a
// domain sentinel so consumers (e.g. the auth middleware) test for it with
// errors.Is without importing the persistence layer's own error types.
var ErrNotFound = errors.New("apikey: not found")

// keyPrefix marks a plaintext key as belonging to this gateway. It is purely
// cosmetic (helps operators recognise the token) and is part of the hashed
// material, so it does not weaken the key.
const keyPrefix = "sk-rag-"

// BudgetPeriod scopes how a key's token budget is measured.
type BudgetPeriod string

const (
	// Monthly resets the allowance at every calendar-month boundary: the budget
	// is compared against tokens consumed since the first of the current month.
	Monthly BudgetPeriod = "monthly"
	// Lifetime is cumulative and never resets: the budget is compared against all
	// tokens the key has ever consumed.
	Lifetime BudgetPeriod = "lifetime"
)

// Valid reports whether p is a recognised period.
func (p BudgetPeriod) Valid() bool {
	return p == Monthly || p == Lifetime
}

// Unlimited is the sentinel token budget meaning "no cap" (on-demand billing).
// Any non-negative value below it would be nonsensical, so -1 is the only
// allowed negative.
const Unlimited int64 = -1

// Priority ranks a key's traffic in the upstream admission queue: when the
// Ollama pool is saturated, higher-priority requests are admitted first. It is
// a scheduling rank, not an entitlement — every key keeps the same batch, rate
// and budget limits, and a low-priority request is never dropped for a
// high-priority one, only served after it (and promoted once it has waited too
// long).
const (
	// NormalPriority is the default rank every key is issued with: free,
	// best-effort access to whatever capacity the pool has spare.
	NormalPriority = 0
	// MaxPriority is the top rank, reserved for the operator's own front-of-house
	// traffic (the main site) so its users never queue behind batch jobs.
	MaxPriority = 9
)

// ValidPriority reports whether p is an assignable priority rank. The ceiling
// exists so the queue's per-class bookkeeping stays a handful of classes rather
// than one per arbitrary integer an operator types.
func ValidPriority(p int) bool { return p >= NormalPriority && p <= MaxPriority }

// APIKey is a credential a client presents plus the limits that govern it. It
// doubles as the GORM model; only the key hash is ever stored, never the
// plaintext. The pure methods below carry the business rules and touch neither
// the database nor the clock, so they are unit-tested in isolation.
type APIKey struct {
	ID         uint   `gorm:"primaryKey"`
	Name       string `gorm:"column:name"`
	KeyHash    string `gorm:"column:key_hash;uniqueIndex"`
	BatchMax   int    `gorm:"column:batch_max"`
	RatePerMin int    `gorm:"column:rate_per_min"`
	// TokenBudget caps bge-m3 input tokens; Unlimited (-1) means no cap.
	TokenBudget  int64        `gorm:"column:token_budget"`
	BudgetPeriod BudgetPeriod `gorm:"column:budget_period"`
	// Priority is the key's rank in the upstream admission queue (see
	// NormalPriority / MaxPriority). Higher is served first when the pool is busy.
	Priority int `gorm:"column:priority"`
	// RevokedAt is nil for an active key and set once revoked; a revoked key is
	// rejected at auth regardless of its limits.
	RevokedAt *time.Time `gorm:"column:revoked_at"`
	CreatedAt time.Time  `gorm:"column:created_at"`
}

// TableName pins the table so GORM's acronym handling ("APIKey") can't surprise
// us; it must match the goose migration.
func (APIKey) TableName() string { return "api_keys" }

// IsUnlimited reports whether the key has no token cap.
func (k APIKey) IsUnlimited() bool { return k.TokenBudget == Unlimited }

// IsRevoked reports whether the key has been revoked.
func (k APIKey) IsRevoked() bool { return k.RevokedAt != nil }

// AllowsBatch reports whether a request with n inputs is within the key's batch
// ceiling. n below 1 is never allowed (an embeddings request needs at least one
// input), which also guards against empty-array requests.
func (k APIKey) AllowsBatch(n int) bool {
	return n >= 1 && n <= k.BatchMax
}

// BudgetExhausted reports whether the key has spent its token allowance. The
// caller supplies both the month-to-date and lifetime totals; this method picks
// the one that matches the key's period so the "which window" rule lives with
// the domain, not scattered across call sites. Unlimited keys are never
// exhausted.
//
// Enforcement is pre-flight and the authoritative bge-m3 count is only known
// after the upstream call (usage is recorded once the response returns), so a
// key can overshoot its budget by the tokens of whatever requests are already
// in flight when the allowance is crossed. For a soft prepaid allowance that
// bounded overshoot is accepted rather than engineered away with a distributed
// token reservation.
func (k APIKey) BudgetExhausted(usedThisMonth, usedLifetime int64) bool {
	if k.IsUnlimited() {
		return false
	}
	used := usedLifetime
	if k.BudgetPeriod == Monthly {
		used = usedThisMonth
	}
	return used >= k.TokenBudget
}

// Remaining returns how many tokens are left against the budget for the key's
// period, clamped at zero. Unlimited keys return Unlimited (-1) to signal "no
// finite remaining".
func (k APIKey) Remaining(usedThisMonth, usedLifetime int64) int64 {
	if k.IsUnlimited() {
		return Unlimited
	}
	used := usedLifetime
	if k.BudgetPeriod == Monthly {
		used = usedThisMonth
	}
	if rem := k.TokenBudget - used; rem > 0 {
		return rem
	}
	return 0
}

// GenerateKey returns a new random plaintext key. It is generated with
// crypto/rand and returned once to the operator; only its hash is persisted.
func GenerateKey() (string, error) {
	// 24 random bytes -> 192 bits of entropy, base32-encoded for a clean,
	// case-tolerant, URL/header-safe token body.
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	body := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return keyPrefix + body, nil
}

// HashKey returns the hex SHA-256 of a plaintext key. It is the only
// representation ever stored or compared, so a database leak never exposes a
// usable credential.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Repository is the persistence port for keys. It is defined here as the
// domain's port; the GORM implementation lives in the platform layer, and
// consumers depend on this interface (or a narrower subset) rather than the
// concrete type.
type Repository interface {
	// Create persists a new key. The caller sets KeyHash and limits.
	Create(ctx context.Context, k *APIKey) error
	// ByHash returns the key with the given hash, or an error if none exists.
	ByHash(ctx context.Context, hash string) (*APIKey, error)
	// ByID returns the key with the given id, or ErrNotFound if none exists.
	ByID(ctx context.Context, id uint) (*APIKey, error)
	// List returns all keys, newest first, for the operator CLI and dashboard.
	List(ctx context.Context) ([]APIKey, error)
	// SetPriority re-ranks a key in the admission queue. It exists so an
	// operator can promote an already-issued key (the main site's) without
	// minting a replacement and re-deploying it.
	SetPriority(ctx context.Context, id uint, priority int) error
	// Revoke marks the key revoked; subsequent auth attempts fail.
	Revoke(ctx context.Context, id uint) error
}
