package postgres

// PostgresFeatures implements DatabaseFeatures for PostgreSQL
type PostgresFeatures struct{}

// SupportsJoinType checks if PostgreSQL supports the given join type
func (f *PostgresFeatures) SupportsJoinType(joinType string) bool {
	switch joinType {
	case "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "NATURAL", "LATERAL":
		return true
	default:
		return false
	}
}

// SupportsWindowFunctions indicates support for window functions
func (f *PostgresFeatures) SupportsWindowFunctions() bool {
	return true
}

// SupportsCTE indicates support for Common Table Expressions
func (f *PostgresFeatures) SupportsCTE() bool {
	return true
}

// SupportsUnion indicates support for UNION operations
func (f *PostgresFeatures) SupportsUnion() bool {
	return true
}

// SupportsDistinctOn indicates support for DISTINCT ON
func (f *PostgresFeatures) SupportsDistinctOn() bool {
	return true
}

// SupportsReturning indicates support for RETURNING clause
func (f *PostgresFeatures) SupportsReturning() bool {
	return true
}

// SupportsJSONOperations indicates support for JSON operations
func (f *PostgresFeatures) SupportsJSONOperations() bool {
	return true
}

// SupportsArrayOperations indicates support for array operations
func (f *PostgresFeatures) SupportsArrayOperations() bool {
	return true
}

// SupportsFullTextSearch indicates support for full-text search
func (f *PostgresFeatures) SupportsFullTextSearch() bool {
	return true
}
