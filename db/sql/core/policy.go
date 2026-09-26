package core

import (
	"errors"
	"reflect"
)

// DataSourcePolicy is what a datasource's configuration lets gorgany do with it beyond
// reading.
//
// The zero value is the datasource gorgany has always assumed: it owns the schema, so
// db:migrate, db:seed, db:diff and the sessions table may create and alter tables in it, and
// it may write rows. Both flags narrow that. They exist for databases gorgany shares with
// something that owns them — tables another application's migrations created (an EF Core
// model, say), or a replica — where one AutoMigrate or one stray write is damage the other
// owner has to repair.
type DataSourcePolicy struct {
	// ExternalSchema says the schema is owned outside gorgany (external_schema: true). Rows
	// may still be read and written, but nothing gorgany runs may create, alter, drop or
	// rename a table, index, constraint or column there. Refusals wrap ErrExternalSchema.
	ExternalSchema bool

	// ReadOnly says nothing may be written through the datasource at all (read_only: true).
	// Refusals wrap ErrReadOnly.
	ReadOnly bool
}

// PolicyReporter is implemented by datasources that know their policy.
//
// It is a separate interface rather than a method on IDataSource so that adding it breaks no
// datasource an app has written: one that does not implement it reports the zero policy, which
// is exactly what gorgany assumed of it before the question could be asked.
type PolicyReporter interface {
	// Policy reports what the datasource's configuration allows.
	Policy() DataSourcePolicy
}

// PolicyOf reports ds's policy, or the zero policy when ds is nil or does not say.
//
// Every caller that is about to change a schema or write through a datasource asks this
// rather than type-asserting PolicyReporter itself, so the answer for a datasource that does
// not implement it — or for a nil one, including a nil pointer of a type that does — is the
// same everywhere.
func PolicyOf(ds IDataSource) DataSourcePolicy {
	if ds == nil {
		return DataSourcePolicy{}
	}
	if value := reflect.ValueOf(ds); value.Kind() == reflect.Pointer && value.IsNil() {
		return DataSourcePolicy{}
	}
	reporter, ok := ds.(PolicyReporter)
	if !ok {
		return DataSourcePolicy{}
	}
	return reporter.Policy()
}

// Refusal returns the sentinel a refusal under p wraps: ErrExternalSchema when the schema is
// owned outside gorgany, else ErrReadOnly when writes are refused, else nil.
//
// external_schema comes first when both flags are set. It is the stronger statement, and the
// one an operator has to change: removing read_only alone would leave the schema off limits.
// Every layer that refuses on policy — the db commands, the provider and the session
// repository — takes the sentinel from here, so they all name the same flag, and a later
// change to what either flag means changes that choice in one place.
func (p DataSourcePolicy) Refusal() error {
	switch {
	case p.ExternalSchema:
		return ErrExternalSchema
	case p.ReadOnly:
		return ErrReadOnly
	}
	return nil
}

// IsExternalSchema reports whether ds's schema is owned outside gorgany.
func IsExternalSchema(ds IDataSource) bool { return PolicyOf(ds).ExternalSchema }

// IsReadOnly reports whether ds refuses writes.
func IsReadOnly(ds IDataSource) bool { return PolicyOf(ds).ReadOnly }

// The sentinels every policy refusal wraps, whichever layer refuses — a command, a provider,
// a session repository, a dialect or a gorm callback — so a caller can tell a refusal from a
// database error with errors.Is and need not match on text. Their text names the setting
// that caused the refusal, since that is what an operator reading it has to change.
var (
	ErrExternalSchema = errors.New("datasource schema is owned outside gorgany (external_schema: true)")
	ErrReadOnly       = errors.New("datasource is read-only (read_only: true)")
)
