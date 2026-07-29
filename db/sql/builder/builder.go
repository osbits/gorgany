// Package builder holds the engine-agnostic SQL query builder.
//
// The builder accumulates clauses into a dbCore.Query and delegates every byte
// of SQL rendering to an injected dbCore.SQLDialect. It contains no
// engine-specific syntax of its own, which is why it lives here rather than
// under db/sql/gorm/postgres/v2 — that path was where consumers went looking for
// the dialect seam and concluded there wasn't one.
package builder

import (
	dbCore "github.com/osbits/gorgany/db/sql/core"
)

// Builder implements the QueryBuilder interface
type Builder struct {
	query   *dbCore.Query
	dialect dbCore.SQLDialect
}

// Dialect returns the dialect this builder renders through.
func (b *Builder) Dialect() dbCore.SQLDialect {
	return b.dialect
}

// Clone creates a deep copy of the Builder
func (b *Builder) Clone() *Builder {
	newBuilder := &Builder{
		query:   b.cloneQuery(),
		dialect: b.dialect,
	}
	return newBuilder
}

// cloneQuery creates a deep copy of the Query
func (b *Builder) cloneQuery() *dbCore.Query {
	if b.query == nil {
		return nil
	}

	newQuery := &dbCore.Query{}

	// Clone Select
	if b.query.Select != nil {
		newQuery.Select = &dbCore.SelectClause{
			Fields:     append([]string{}, b.query.Select.Fields...),
			Distinct:   b.query.Select.Distinct,
			DistinctOn: append([]string{}, b.query.Select.DistinctOn...),
		}
	}

	// Clone From
	if b.query.From != nil {
		newFrom := &dbCore.FromClause{
			Table:      b.query.From.Table,
			Alias:      b.query.From.Alias,
			IsSubquery: b.query.From.IsSubquery,
		}
		if b.query.From.Subquery != nil {
			// Deep copy the subquery
			newFrom.Subquery = b.cloneQueryRecursive(b.query.From.Subquery)
		}
		newQuery.From = newFrom
	}

	// Clone Where
	if b.query.Where != nil {
		newWhere := &dbCore.WhereClause{
			Operator: b.query.Where.Operator,
		}
		if len(b.query.Where.Conditions) > 0 {
			newWhere.Conditions = make([]dbCore.Condition, len(b.query.Where.Conditions))
			for i, condition := range b.query.Where.Conditions {
				// For simplicity, we're not deep copying conditions
				// This is a limitation of the current implementation
				newWhere.Conditions[i] = condition
			}
		}
		newQuery.Where = newWhere
	}

	// Clone Joins
	if len(b.query.Joins) > 0 {
		newQuery.Joins = make([]*dbCore.JoinClause, len(b.query.Joins))
		for i, join := range b.query.Joins {
			newJoin := &dbCore.JoinClause{
				Type:       join.Type,
				Table:      join.Table,
				Alias:      join.Alias,
				Condition:  join.Condition, // Not deep copying condition
				IsSubquery: join.IsSubquery,
				IsLateral:  join.IsLateral,
			}
			if join.Subquery != nil {
				newJoin.Subquery = b.cloneQueryRecursive(join.Subquery)
			}
			newQuery.Joins[i] = newJoin
		}
	}

	// Clone OrderBy
	if b.query.OrderBy != nil {
		newOrderBy := &dbCore.OrderByClause{}
		if len(b.query.OrderBy.Fields) > 0 {
			newOrderBy.Fields = make([]dbCore.OrderByField, len(b.query.OrderBy.Fields))
			for i, field := range b.query.OrderBy.Fields {
				newOrderBy.Fields[i] = dbCore.OrderByField{
					Field:     field.Field,
					Direction: field.Direction,
				}
			}
		}
		newQuery.OrderBy = newOrderBy
	}

	// Clone GroupBy
	if b.query.GroupBy != nil {
		newGroupBy := &dbCore.GroupByClause{
			Fields: append([]string{}, b.query.GroupBy.Fields...),
			Rollup: append([]string{}, b.query.GroupBy.Rollup...),
			Cube:   append([]string{}, b.query.GroupBy.Cube...),
		}
		if len(b.query.GroupBy.Sets) > 0 {
			newGroupBy.Sets = make([][]string, len(b.query.GroupBy.Sets))
			for i, set := range b.query.GroupBy.Sets {
				newGroupBy.Sets[i] = append([]string{}, set...)
			}
		}
		newQuery.GroupBy = newGroupBy
	}

	// Clone Having
	if b.query.Having != nil {
		newQuery.Having = &dbCore.HavingClause{
			Condition: b.query.Having.Condition, // Not deep copying condition
		}
	}

	// Clone Limit
	if b.query.Limit != nil {
		limit := *b.query.Limit
		newQuery.Limit = &limit
	}

	// Clone Offset
	if b.query.Offset != nil {
		offset := *b.query.Offset
		newQuery.Offset = &offset
	}

	// Clone CTEs
	if len(b.query.CTEs) > 0 {
		newQuery.CTEs = make([]*dbCore.CTEClause, len(b.query.CTEs))
		for i, cte := range b.query.CTEs {
			newCTE := &dbCore.CTEClause{
				Name: cte.Name,
			}
			if cte.Query != nil {
				newCTE.Query = b.cloneQueryRecursive(cte.Query)
			}
			newQuery.CTEs[i] = newCTE
		}
	}

	// Clone Unions
	if len(b.query.Unions) > 0 {
		newQuery.Unions = make([]*dbCore.UnionClause, len(b.query.Unions))
		for i, union := range b.query.Unions {
			newUnion := &dbCore.UnionClause{
				All: union.All,
			}
			if union.Query != nil {
				newUnion.Query = b.cloneQueryRecursive(union.Query)
			}
			newQuery.Unions[i] = newUnion
		}
	}

	// Clone Windows
	if len(b.query.Windows) > 0 {
		newQuery.Windows = make([]*dbCore.WindowClause, len(b.query.Windows))
		for i, window := range b.query.Windows {
			newWindow := &dbCore.WindowClause{
				Name: window.Name,
			}
			if window.Definition != nil {
				newDef := &dbCore.WindowDefinition{
					PartitionBy: append([]string{}, window.Definition.PartitionBy...),
				}
				if len(window.Definition.OrderBy) > 0 {
					newDef.OrderBy = make([]dbCore.OrderByField, len(window.Definition.OrderBy))
					for j, field := range window.Definition.OrderBy {
						newDef.OrderBy[j] = dbCore.OrderByField{
							Field:     field.Field,
							Direction: field.Direction,
						}
					}
				}
				if window.Definition.Frame != nil {
					newFrame := &dbCore.WindowFrame{
						Type:      window.Definition.Frame.Type,
						Exclusion: window.Definition.Frame.Exclusion,
					}
					if window.Definition.Frame.Start != nil {
						newFrame.Start = &dbCore.FrameBound{
							Type:  window.Definition.Frame.Start.Type,
							Value: window.Definition.Frame.Start.Value,
						}
					}
					if window.Definition.Frame.End != nil {
						newFrame.End = &dbCore.FrameBound{
							Type:  window.Definition.Frame.End.Type,
							Value: window.Definition.Frame.End.Value,
						}
					}
					newDef.Frame = newFrame
				}
				newWindow.Definition = newDef
			}
			newQuery.Windows[i] = newWindow
		}
	}

	// Clone Returning
	if len(b.query.Returning) > 0 {
		newQuery.Returning = append([]string{}, b.query.Returning...)
	}

	// Clone Insert
	if b.query.Insert != nil {
		newInsert := &dbCore.InsertClause{
			Table:   b.query.Insert.Table,
			Columns: append([]string{}, b.query.Insert.Columns...),
		}
		if len(b.query.Insert.Values) > 0 {
			newInsert.Values = make([][]interface{}, len(b.query.Insert.Values))
			for i, row := range b.query.Insert.Values {
				newInsert.Values[i] = append([]interface{}{}, row...)
			}
		}
		if b.query.Insert.FromQuery != nil {
			newInsert.FromQuery = b.cloneQueryRecursive(b.query.Insert.FromQuery)
		}
		if b.query.Insert.OnConflict != nil {
			newOnConflict := &dbCore.OnConflictClause{
				Columns: append([]string{}, b.query.Insert.OnConflict.Columns...),
				Action:  b.query.Insert.OnConflict.Action,
			}
			if b.query.Insert.OnConflict.SetValues != nil {
				newOnConflict.SetValues = make(map[string]interface{})
				for k, v := range b.query.Insert.OnConflict.SetValues {
					newOnConflict.SetValues[k] = v
				}
			}
			newInsert.OnConflict = newOnConflict
		}
		newQuery.Insert = newInsert
	}

	// Clone Update
	if b.query.Update != nil {
		newUpdate := &dbCore.UpdateClause{
			Table:  b.query.Update.Table,
			Values: make(map[string]interface{}),
		}
		for k, v := range b.query.Update.Values {
			newUpdate.Values[k] = v
		}
		newQuery.Update = newUpdate
	}

	// Clone Delete
	if b.query.Delete != nil {
		newQuery.Delete = &dbCore.DeleteClause{
			Table: b.query.Delete.Table,
		}
	}

	return newQuery
}

// cloneQueryRecursive is a helper function to clone a Query recursively
func (b *Builder) cloneQueryRecursive(query *dbCore.Query) *dbCore.Query {
	if query == nil {
		return nil
	}

	// Create a temporary Builder with the query to clone
	tempBuilder := &Builder{
		query:   query,
		dialect: b.dialect,
	}

	// Use the cloneQuery method to create a deep copy
	return tempBuilder.cloneQuery()
}

// Config configures a Builder.
//
// This type existed before v2 but nothing constructed or read it — NewBuilder
// hard-coded &PostgresDialect{}, so the only way to reach another engine was to
// fork the builder. New/NewFromConfig make it live.
type Config struct {
	// Dialect renders the accumulated query as SQL. Required.
	Dialect dbCore.SQLDialect
}

// New returns a Builder that renders through dialect.
//
// dialect must not be nil. A nil dialect is a programming error, not a
// configuration error: there is no sensible default (picking one silently is how
// the Postgres dialect ended up welded in), and the alternative is a nil
// dereference later, inside ToSQL, far from the call site that caused it.
func New(dialect dbCore.SQLDialect) *Builder {
	if dialect == nil {
		panic("gorgany/db/sql/builder: New requires a non-nil dialect")
	}
	return &Builder{
		query:   &dbCore.Query{},
		dialect: dialect,
	}
}

// NewFromConfig is New driven by a Config value, for callers that thread
// configuration around as a struct.
func NewFromConfig(cfg Config) *Builder {
	return New(cfg.Dialect)
}

// NewLike returns a fresh, empty builder speaking the same dialect as b.
//
// It exists for code that is handed a builder and needs a second, independent one
// — building a sub-condition, or a subquery — without having to know which engine
// it is talking to. Before this, such code called the Postgres constructor
// directly and silently emitted Postgres SQL onto whatever connection it was
// given.
func NewLike(b dbCore.IQueryBuilder) *Builder {
	if b == nil {
		panic("gorgany/db/sql/builder: NewLike requires a non-nil builder")
	}
	return New(b.Dialect())
}

// Select adds fields to the SELECT clause
func (b *Builder) Select(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Select == nil {
		newBuilder.query.Select = &dbCore.SelectClause{}
	}
	newBuilder.query.Select.Fields = append(newBuilder.query.Select.Fields, fields...)
	return newBuilder
}

// From sets the FROM clause
func (b *Builder) From(table string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.From = &dbCore.FromClause{
		Table: table,
	}
	return newBuilder
}

// Where adds a condition to the WHERE clause
func (b *Builder) Where(condition dbCore.Condition) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Where == nil {
		newBuilder.query.Where = &dbCore.WhereClause{
			Operator: "AND",
		}
	}
	newBuilder.query.Where.Conditions = append(newBuilder.query.Where.Conditions, condition)
	return newBuilder
}

// Join adds a JOIN clause
func (b *Builder) Join(join *dbCore.JoinClause) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Joins = append(newBuilder.query.Joins, join)
	return newBuilder
}

// OrderBy adds an ORDER BY clause. field is treated as untrusted data: a simple
// or dotted identifier is emitted quoted, and any other value is bound as a
// placeholder rather than interpolated (so request-derived input cannot inject).
// For a trusted SQL expression, use OrderByRaw.
func (b *Builder) OrderBy(field string, direction string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.OrderBy == nil {
		newBuilder.query.OrderBy = &dbCore.OrderByClause{}
	}
	newBuilder.query.OrderBy.Fields = append(newBuilder.query.OrderBy.Fields, dbCore.OrderByField{
		Field:     field,
		Direction: direction,
	})
	return newBuilder
}

// OrderByRaw adds an ORDER BY clause whose expression is emitted verbatim
// (not quoted, not parameterized). Use only with a trusted, caller-supplied
// expression such as "first_name || ' ' || last_name" — never with
// request-derived input.
func (b *Builder) OrderByRaw(expression string, direction string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.OrderBy == nil {
		newBuilder.query.OrderBy = &dbCore.OrderByClause{}
	}
	newBuilder.query.OrderBy.Fields = append(newBuilder.query.OrderBy.Fields, dbCore.OrderByField{
		Field:     expression,
		Direction: direction,
		Raw:       true,
	})
	return newBuilder
}

// GroupBy adds a GROUP BY clause
func (b *Builder) GroupBy(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.GroupBy == nil {
		newBuilder.query.GroupBy = &dbCore.GroupByClause{}
	}
	newBuilder.query.GroupBy.Fields = append(newBuilder.query.GroupBy.Fields, fields...)
	return newBuilder
}

// Having adds a HAVING clause
func (b *Builder) Having(condition dbCore.Condition) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Having = &dbCore.HavingClause{
		Condition: condition,
	}
	return newBuilder
}

// Limit sets the LIMIT clause
func (b *Builder) Limit(limit int) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Limit = &limit
	return newBuilder
}

// Offset sets the OFFSET clause
func (b *Builder) Offset(offset int) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Offset = &offset
	return newBuilder
}

// WithCTE adds a Common Table Expression
func (b *Builder) WithCTE(name string, query *dbCore.Query) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.CTEs = append(newBuilder.query.CTEs, &dbCore.CTEClause{
		Name:  name,
		Query: query,
	})
	return newBuilder
}

// Union adds a UNION clause
func (b *Builder) Union(query *dbCore.Query) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Unions = append(newBuilder.query.Unions, &dbCore.UnionClause{
		Query: query,
		All:   false,
	})
	return newBuilder
}

// UnionAll adds a UNION ALL clause
func (b *Builder) UnionAll(query *dbCore.Query) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Unions = append(newBuilder.query.Unions, &dbCore.UnionClause{
		Query: query,
		All:   true,
	})
	return newBuilder
}

// Window adds a window function definition
func (b *Builder) Window(name string, definition *dbCore.WindowDefinition) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Windows = append(newBuilder.query.Windows, &dbCore.WindowClause{
		Name:       name,
		Definition: definition,
	})
	return newBuilder
}

// Subquery creates a subquery in the FROM clause
func (b *Builder) Subquery(query *dbCore.Query, alias string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.From = &dbCore.FromClause{
		Subquery:   query,
		Alias:      alias,
		IsSubquery: true,
	}
	return newBuilder
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
	newBuilder := b.Clone()
	if newBuilder.query.GroupBy == nil {
		newBuilder.query.GroupBy = &dbCore.GroupByClause{}
	}
	newBuilder.query.GroupBy.Sets = sets
	return newBuilder
}

func (b *Builder) Rollup(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.GroupBy == nil {
		newBuilder.query.GroupBy = &dbCore.GroupByClause{}
	}
	newBuilder.query.GroupBy.Rollup = fields
	return newBuilder
}

func (b *Builder) Cube(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.GroupBy == nil {
		newBuilder.query.GroupBy = &dbCore.GroupByClause{}
	}
	newBuilder.query.GroupBy.Cube = fields
	return newBuilder
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
	newBuilder := b.Clone()
	newBuilder.query.Joins = append(newBuilder.query.Joins, &dbCore.JoinClause{
		Type:       "LATERAL",
		Subquery:   query,
		Alias:      alias,
		Condition:  condition,
		IsSubquery: true,
		IsLateral:  true,
	})
	return newBuilder
}

// DistinctOn adds a DISTINCT ON clause
func (b *Builder) DistinctOn(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Select == nil {
		newBuilder.query.Select = &dbCore.SelectClause{}
	}
	newBuilder.query.Select.DistinctOn = fields
	return newBuilder
}

// Returning adds a RETURNING clause
func (b *Builder) Returning(fields ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Returning = fields
	return newBuilder
}

// ToSQL renders the query through the builder's dialect.
//
// The error is non-nil when the dialect cannot express the query (see
// dbCore.UnsupportedError); the SQL string is then empty rather than invalid.
func (b *Builder) ToSQL() (string, []interface{}, error) {
	sql, args, err := b.dialect.FormatQuery(b.query)
	if err != nil {
		return "", nil, err
	}
	return sql, args, nil
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

// Insert starts building an INSERT query.
//
// Like every other clause method it returns a clone and leaves the receiver
// untouched. It used to mutate the receiver in place and return b, which was the
// single hole in the builder's copy-on-write contract: an Insert issued against
// a shared builder contaminated every later query built from it.
func (b *Builder) Insert(table string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Insert = &dbCore.InsertClause{
		Table: table,
	}
	return newBuilder
}

// Columns specifies the columns for an INSERT operation
func (b *Builder) Columns(columns ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil {
		return newBuilder
	}
	newBuilder.query.Insert.Columns = columns
	return newBuilder
}

// Values adds a row of values for an INSERT operation
func (b *Builder) Values(values ...interface{}) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil {
		return newBuilder
	}
	newBuilder.query.Insert.Values = append(newBuilder.query.Insert.Values, values)
	return newBuilder
}

// FromSelect specifies a SELECT query to use as the source for an INSERT operation
func (b *Builder) FromSelect(query *dbCore.Query) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil {
		return newBuilder
	}
	newBuilder.query.Insert.FromQuery = query
	return newBuilder
}

// OnConflict starts building an ON CONFLICT clause
func (b *Builder) OnConflict(columns ...string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil {
		return newBuilder
	}
	newBuilder.query.Insert.OnConflict = &dbCore.OnConflictClause{
		Columns: columns,
	}
	return newBuilder
}

// DoNothing completes an ON CONFLICT clause with DO NOTHING
func (b *Builder) DoNothing() dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil || newBuilder.query.Insert.OnConflict == nil {
		return newBuilder
	}
	newBuilder.query.Insert.OnConflict.Action = "DO NOTHING"
	return newBuilder
}

// DoUpdate completes an ON CONFLICT clause with DO UPDATE SET
func (b *Builder) DoUpdate(setValues map[string]interface{}) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Insert == nil || newBuilder.query.Insert.OnConflict == nil {
		return newBuilder
	}
	newBuilder.query.Insert.OnConflict.Action = "DO UPDATE"
	newBuilder.query.Insert.OnConflict.SetValues = setValues
	return newBuilder
}

// Update starts building an UPDATE query
func (b *Builder) Update(table string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Update = &dbCore.UpdateClause{
		Table:  table,
		Values: make(map[string]interface{}),
	}
	return newBuilder
}

// Set adds field=value pairs to an UPDATE operation
func (b *Builder) Set(field string, value interface{}) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Update == nil {
		return newBuilder
	}
	newBuilder.query.Update.Values[field] = value
	return newBuilder
}

// SetMap adds multiple field=value pairs to an UPDATE operation
func (b *Builder) SetMap(values map[string]interface{}) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	if newBuilder.query.Update == nil {
		return newBuilder
	}
	for k, v := range values {
		newBuilder.query.Update.Values[k] = v
	}
	return newBuilder
}

// Delete starts building a DELETE query
func (b *Builder) Delete(table string) dbCore.IQueryBuilder {
	newBuilder := b.Clone()
	newBuilder.query.Delete = &dbCore.DeleteClause{
		Table: table,
	}
	return newBuilder
}

// HasFrom checks if the FROM clause has been set
func (b *Builder) HasFrom() bool {
	return b.query.From != nil
}

// HasWhere checks if the WHERE clause has been set
func (b *Builder) HasWhere() bool {
	return b.query.Where != nil && len(b.query.Where.Conditions) > 0
}

// HasJoin checks if any JOIN clauses have been set
func (b *Builder) HasJoin() bool {
	return len(b.query.Joins) > 0
}

// HasOrderBy checks if the ORDER BY clause has been set
func (b *Builder) HasOrderBy() bool {
	return b.query.OrderBy != nil && len(b.query.OrderBy.Fields) > 0
}

// HasGroupBy checks if the GROUP BY clause has been set
func (b *Builder) HasGroupBy() bool {
	return b.query.GroupBy != nil && (len(b.query.GroupBy.Fields) > 0 ||
		len(b.query.GroupBy.Sets) > 0 ||
		len(b.query.GroupBy.Rollup) > 0 ||
		len(b.query.GroupBy.Cube) > 0)
}

// HasLimit checks if the LIMIT clause has been set
func (b *Builder) HasLimit() bool {
	return b.query.Limit != nil
}

// HasOffset checks if the OFFSET clause has been set
func (b *Builder) HasOffset() bool {
	return b.query.Offset != nil
}
