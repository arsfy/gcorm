package runtime

import (
	gcoerr "github.com/arsfy/gco-orm/pkg/runtime/errors"
)

// DBError is a type alias for the classified database error from the errors
// package. It carries an ErrorCode, message, optional detail/meta, and the
// original cause.
type DBError = gcoerr.DBError

// Dialect abstracts database-specific SQL differences.
type Dialect interface {
	// Name returns the dialect name (e.g., "postgresql", "mysql", "sqlite").
	Name() string

	// Placeholder returns the parameter placeholder for the nth parameter (1-based).
	// PostgreSQL: $1, $2, ...
	// MySQL/SQLite: ?
	Placeholder(n int) string

	// QuoteIdent quotes one or more identifier parts.
	// e.g., QuoteIdent("public", "users") → "public"."users" (PostgreSQL)
	QuoteIdent(parts ...string) string

	// SupportsReturning indicates if the dialect supports RETURNING clause.
	SupportsReturning() bool

	// SupportsSchemas indicates if the dialect supports database schemas/namespaces.
	SupportsSchemas() bool

	// RewriteLimitOffset rewrites LIMIT/OFFSET for the dialect.
	RewriteLimitOffset(limit, offset *int) string

	// ClassifyError maps a raw database error to a structured DBError.
	// Returns nil when the error is not recognized by this dialect.
	ClassifyError(err error) *DBError

	// TypeMapping returns the SQL type for a schema type name.
	TypeMapping(schemaType string, isOptional bool) string

	// DefaultValueExpression returns the SQL expression for a default value.
	DefaultValueExpression(funcName string, args []string) string

	// SupportsFeature checks if a specific feature is supported.
	SupportsFeature(feature Feature) bool
}

// Feature represents a database feature that may vary across dialects.
type Feature int

const (
	FeatureReturning Feature = iota
	FeatureSchemas
	FeatureJSON
	FeatureUUID
	FeatureArrays
	FeatureEnumType
	FeatureCascadeDelete
	FeatureSerialType
	FeatureIdentityColumn
	FeatureUpsert
	FeaturePartialIndex
)
