package v2

import (
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
)

// Builder implements the QueryBuilder interface
type Builder struct {
	query   *dbCore.Query
	dialect dbCore.SQLDialect
}

// NewBuilder creates a new query builder
func NewBuilder() *Builder {
	return &Builder{
		query:   &dbCore.Query{},
		dialect: &PostgresDialect{},
	}
}

type Config struct {
	Dialect dbCore.SQLDialect
}

// NewBuilderWithConfig creates a new query builder with v2.Config
func NewBuilderWithConfig(config Config) *Builder {
	return &Builder{
		query:   &dbCore.Query{},
		dialect: config.Dialect,
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

// ToSQL converts the query to SQL and returns both the SQL string and arguments
func (b *Builder) ToSQL() (string, []interface{}) {
	return b.dialect.FormatQuery(b.query)
}

// Condition helper methods
func (b *Builder) Eq(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: "=",
		Right:    value,
	})
}

func (b *Builder) Neq(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: "!=",
		Right:    value,
	})
}

func (b *Builder) Gt(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: ">",
		Right:    value,
	})
}

func (b *Builder) Gte(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: ">=",
		Right:    value,
	})
}

func (b *Builder) Lt(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: "<",
		Right:    value,
	})
}

func (b *Builder) Lte(field interface{}, value interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BinaryCondition{
		Left:     field,
		Operator: "<=",
		Right:    value,
	})
}

func (b *Builder) In(field interface{}, values ...interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.InCondition{
		Field:  field,
		Values: values,
	})
}

func (b *Builder) NotIn(field interface{}, values ...interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.InCondition{
		Field:  field,
		Values: values,
		Not:    true,
	})
}

func (b *Builder) InSubquery(field interface{}, subquery *dbCore.Query) dbCore.IQueryBuilder {
	return b.Where(&dbCore.InCondition{
		Field:      field,
		IsSubquery: true,
		Subquery:   subquery,
	})
}

func (b *Builder) NotInSubquery(field interface{}, subquery *dbCore.Query) dbCore.IQueryBuilder {
	return b.Where(&dbCore.InCondition{
		Field:      field,
		IsSubquery: true,
		Subquery:   subquery,
		Not:        true,
	})
}

func (b *Builder) Between(field interface{}, lower interface{}, upper interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BetweenCondition{
		Field: field,
		Lower: lower,
		Upper: upper,
	})
}

func (b *Builder) NotBetween(field interface{}, lower interface{}, upper interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.BetweenCondition{
		Field: field,
		Lower: lower,
		Upper: upper,
		Not:   true,
	})
}

func (b *Builder) Exists(subquery *dbCore.Query) dbCore.IQueryBuilder {
	return b.Where(&dbCore.ExistsCondition{
		Query: subquery,
	})
}

func (b *Builder) NotExists(subquery *dbCore.Query) dbCore.IQueryBuilder {
	return b.Where(&dbCore.ExistsCondition{
		Query: subquery,
		Not:   true,
	})
}

func (b *Builder) Like(field interface{}, pattern interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.LikeCondition{
		Field:   field,
		Pattern: pattern,
	})
}

func (b *Builder) NotLike(field interface{}, pattern interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.LikeCondition{
		Field:   field,
		Pattern: pattern,
		Not:     true,
	})
}

func (b *Builder) LikeEscape(field interface{}, pattern interface{}, escape string) dbCore.IQueryBuilder {
	return b.Where(&dbCore.LikeCondition{
		Field:   field,
		Pattern: pattern,
		Escape:  escape,
	})
}

func (b *Builder) NotLikeEscape(field interface{}, pattern interface{}, escape string) dbCore.IQueryBuilder {
	return b.Where(&dbCore.LikeCondition{
		Field:   field,
		Pattern: pattern,
		Escape:  escape,
		Not:     true,
	})
}

func (b *Builder) IsNull(field interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.IsNullCondition{
		Field: field,
	})
}

func (b *Builder) IsNotNull(field interface{}) dbCore.IQueryBuilder {
	return b.Where(&dbCore.IsNullCondition{
		Field: field,
		Not:   true,
	})
}
