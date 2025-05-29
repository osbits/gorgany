package v2

import (
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	"strings"
)

// Builder implements the QueryBuilder interface
type Builder struct {
	query   *dbCore.Query
	dialect *PostgresDialect
}

// NewBuilder creates a new query builder
func NewBuilder() *Builder {
	return &Builder{
		query:   &dbCore.Query{},
		dialect: &PostgresDialect{},
	}
}

// Select adds fields to the SELECT clause
func (b *Builder) Select(fields ...string) dbCore.IQueryBuilder {
	if b.query.Select == nil {
		b.query.Select = &dbCore.SelectClause{}
	}
	b.query.Select.Fields = append(b.query.Select.Fields, fields...)
	return b
}

// From sets the FROM clause
func (b *Builder) From(table string) dbCore.IQueryBuilder {
	b.query.From = &dbCore.FromClause{
		Table: table,
	}
	return b
}

// Where adds a condition to the WHERE clause
func (b *Builder) Where(condition dbCore.Condition) dbCore.IQueryBuilder {
	if b.query.Where == nil {
		b.query.Where = &dbCore.WhereClause{
			Operator: "AND",
		}
	}
	b.query.Where.Conditions = append(b.query.Where.Conditions, condition)
	return b
}

// Join adds a JOIN clause
func (b *Builder) Join(join *dbCore.JoinClause) dbCore.IQueryBuilder {
	b.query.Joins = append(b.query.Joins, join)
	return b
}

// OrderBy adds an ORDER BY clause
func (b *Builder) OrderBy(field string, direction string) dbCore.IQueryBuilder {
	if b.query.OrderBy == nil {
		b.query.OrderBy = &dbCore.OrderByClause{}
	}
	b.query.OrderBy.Fields = append(b.query.OrderBy.Fields, dbCore.OrderByField{
		Field:     field,
		Direction: direction,
	})
	return b
}

// GroupBy adds a GROUP BY clause
func (b *Builder) GroupBy(fields ...string) dbCore.IQueryBuilder {
	if b.query.GroupBy == nil {
		b.query.GroupBy = &dbCore.GroupByClause{}
	}
	b.query.GroupBy.Fields = append(b.query.GroupBy.Fields, fields...)
	return b
}

// Having adds a HAVING clause
func (b *Builder) Having(condition dbCore.Condition) dbCore.IQueryBuilder {
	b.query.Having = &dbCore.HavingClause{
		Condition: condition,
	}
	return b
}

// Limit sets the LIMIT clause
func (b *Builder) Limit(limit int) dbCore.IQueryBuilder {
	b.query.Limit = &limit
	return b
}

// Offset sets the OFFSET clause
func (b *Builder) Offset(offset int) dbCore.IQueryBuilder {
	b.query.Offset = &offset
	return b
}

// WithCTE adds a Common Table Expression
func (b *Builder) WithCTE(name string, query *dbCore.Query) dbCore.IQueryBuilder {
	b.query.CTEs = append(b.query.CTEs, &dbCore.CTEClause{
		Name:  name,
		Query: query,
	})
	return b
}

// Union adds a UNION clause
func (b *Builder) Union(query *dbCore.Query) dbCore.IQueryBuilder {
	b.query.Unions = append(b.query.Unions, &dbCore.UnionClause{
		Query: query,
		All:   false,
	})
	return b
}

// UnionAll adds a UNION ALL clause
func (b *Builder) UnionAll(query *dbCore.Query) dbCore.IQueryBuilder {
	b.query.Unions = append(b.query.Unions, &dbCore.UnionClause{
		Query: query,
		All:   true,
	})
	return b
}

// Window adds a window function definition
func (b *Builder) Window(name string, definition *dbCore.WindowDefinition) dbCore.IQueryBuilder {
	b.query.Windows = append(b.query.Windows, &dbCore.WindowClause{
		Name:       name,
		Definition: definition,
	})
	return b
}

// Subquery creates a subquery in the FROM clause
func (b *Builder) Subquery(query *dbCore.Query, alias string) dbCore.IQueryBuilder {
	b.query.From = &dbCore.FromClause{
		Subquery:   query,
		Alias:      alias,
		IsSubquery: true,
	}
	return b
}

// Build returns the final query
func (b *Builder) Build() *dbCore.Query {
	return b.query
}

// Helper methods for creating joins
func (b *Builder) InnerJoin(table string, condition dbCore.Condition) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:      "INNER",
		Table:     table,
		Condition: condition,
	})
}

func (b *Builder) LeftJoin(table string, condition dbCore.Condition) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:      "LEFT",
		Table:     table,
		Condition: condition,
	})
}

func (b *Builder) RightJoin(table string, condition dbCore.Condition) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:      "RIGHT",
		Table:     table,
		Condition: condition,
	})
}

func (b *Builder) FullJoin(table string, condition dbCore.Condition) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:      "FULL",
		Table:     table,
		Condition: condition,
	})
}

func (b *Builder) CrossJoin(table string) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:  "CROSS",
		Table: table,
	})
}

func (b *Builder) NaturalJoin(table string) dbCore.IQueryBuilder {
	return b.Join(&dbCore.JoinClause{
		Type:  "NATURAL",
		Table: table,
	})
}

// Helper methods for GROUP BY clauses
func (b *Builder) GroupingSets(sets ...[]string) dbCore.IQueryBuilder {
	if b.query.GroupBy == nil {
		b.query.GroupBy = &dbCore.GroupByClause{}
	}
	b.query.GroupBy.Sets = sets
	return b
}

func (b *Builder) Rollup(fields ...string) dbCore.IQueryBuilder {
	if b.query.GroupBy == nil {
		b.query.GroupBy = &dbCore.GroupByClause{}
	}
	b.query.GroupBy.Rollup = fields
	return b
}

func (b *Builder) Cube(fields ...string) dbCore.IQueryBuilder {
	if b.query.GroupBy == nil {
		b.query.GroupBy = &dbCore.GroupByClause{}
	}
	b.query.GroupBy.Cube = fields
	return b
}

// Helper methods for window functions
func (b *Builder) Over(name string) string {
	return name + " OVER (" + b.buildWindowDefinition(name) + ")"
}

func (b *Builder) buildWindowDefinition(name string) string {
	for _, window := range b.query.Windows {
		if window.Name == name {
			return window.Definition.String()
		}
	}
	return ""
}

// LateralJoin adds a LATERAL JOIN clause
func (b *Builder) LateralJoin(query *dbCore.Query, alias string, condition dbCore.Condition) dbCore.IQueryBuilder {
	b.query.Joins = append(b.query.Joins, &dbCore.JoinClause{
		Type:       "LATERAL",
		Subquery:   query,
		Alias:      alias,
		Condition:  condition,
		IsSubquery: true,
		IsLateral:  true,
	})
	return b
}

// DistinctOn adds a DISTINCT ON clause
func (b *Builder) DistinctOn(fields ...string) dbCore.IQueryBuilder {
	if b.query.Select == nil {
		b.query.Select = &dbCore.SelectClause{}
	}
	b.query.Select.DistinctOn = fields
	return b
}

// Returning adds a RETURNING clause
func (b *Builder) Returning(fields ...string) dbCore.IQueryBuilder {
	b.query.Returning = fields
	return b
}

// ToSQL converts the query to SQL
func (b *Builder) ToSQL() string {
	var parts []string

	// Add CTEs if any
	if len(b.query.CTEs) > 0 {
		cteParts := make([]string, len(b.query.CTEs))
		for i, cte := range b.query.CTEs {
			cteParts[i] = b.dialect.FormatCTE(cte.Name, cte.Query)
		}
		parts = append(parts, "WITH "+strings.Join(cteParts, ", "))
	}

	// Add SELECT clause
	if b.query.Select != nil {
		parts = append(parts, b.dialect.FormatSelect(
			b.query.Select.Fields,
			b.query.Select.Distinct,
			b.query.Select.DistinctOn,
		))
	}

	// Add FROM clause
	if b.query.From != nil {
		parts = append(parts, b.dialect.FormatFrom(b.query.From.Table, b.query.From.Alias))
	}

	// Add JOINs
	for _, join := range b.query.Joins {
		parts = append(parts, b.dialect.FormatJoin(join))
	}

	// Add WHERE clause
	if b.query.Where != nil {
		whereSQL := b.dialect.FormatWhere(b.query.Where)
		if whereSQL != "" {
			parts = append(parts, whereSQL)
		}
	}

	// Add GROUP BY clause
	if b.query.GroupBy != nil {
		parts = append(parts, b.dialect.FormatGroupBy(b.query.GroupBy.Fields))
	}

	// Add HAVING clause
	if b.query.Having != nil {
		havingSQL := b.dialect.FormatHaving(b.query.Having)
		if havingSQL != "" {
			parts = append(parts, havingSQL)
		}
	}

	// Add ORDER BY clause
	if b.query.OrderBy != nil {
		for _, field := range b.query.OrderBy.Fields {
			parts = append(parts, b.dialect.FormatOrderBy(field.Field, field.Direction))
		}
	}

	// Add LIMIT clause
	if b.query.Limit != nil {
		parts = append(parts, b.dialect.FormatLimit(*b.query.Limit))
	}

	// Add OFFSET clause
	if b.query.Offset != nil {
		parts = append(parts, b.dialect.FormatOffset(*b.query.Offset))
	}

	// Add UNION clauses
	for _, union := range b.query.Unions {
		parts = append(parts, b.dialect.FormatUnion(union.Query, union.All))
	}

	// Add RETURNING clause
	if len(b.query.Returning) > 0 {
		parts = append(parts, b.dialect.FormatReturning(b.query.Returning))
	}

	return strings.Join(parts, " ")
}
