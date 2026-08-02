package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	assert.NotNil(t, builder)
	assert.NotNil(t, builder.Build())
	assert.IsType(t, &PostgresDialect{}, builder.Dialect())
}

func TestBuilder_Select(t *testing.T) {
	builder := NewBuilder().Select("id", "name", "email")
	q := builder.Build()
	assert.NotNil(t, q.Select)
	assert.Equal(t, []string{"id", "name", "email"}, q.Select.Fields)
}

func TestBuilder_From(t *testing.T) {
	builder := NewBuilder().From("users")
	q := builder.Build()

	assert.NotNil(t, q.From)
	assert.Equal(t, "users", q.From.Table)
}

func TestBuilder_Where(t *testing.T) {
	condition := &dbCore.BinaryCondition{
		Left:     "id",
		Operator: "=",
		Right:    1,
	}
	builder := NewBuilder().Where(condition)
	q := builder.Build()

	assert.NotNil(t, q.Where)
	assert.Equal(t, "AND", q.Where.Operator)
	assert.Len(t, q.Where.Conditions, 1)
	assert.Equal(t, condition, q.Where.Conditions[0])
}

func TestBuilder_Join(t *testing.T) {
	join := &dbCore.JoinClause{
		Type:  "INNER",
		Table: "orders",
		Condition: &dbCore.BinaryCondition{
			Left:     "users.id",
			Operator: "=",
			Right:    "orders.user_id",
		},
	}
	builder := NewBuilder().Join(join)
	q := builder.Build()

	assert.Len(t, q.Joins, 1)
	assert.Equal(t, join, q.Joins[0])
}

func TestBuilder_OrderBy(t *testing.T) {
	builder := NewBuilder().OrderBy("created_at", "DESC")
	q := builder.Build()

	assert.NotNil(t, q.OrderBy)
	assert.Len(t, q.OrderBy.Fields, 1)
	assert.Equal(t, "created_at", q.OrderBy.Fields[0].Field)
	assert.Equal(t, "DESC", q.OrderBy.Fields[0].Direction)
}

func TestBuilder_GroupBy(t *testing.T) {
	builder := NewBuilder().GroupBy("status", "type")
	q := builder.Build()

	assert.NotNil(t, q.GroupBy)
	assert.Equal(t, []string{"status", "type"}, q.GroupBy.Fields)
}

func TestBuilder_Having(t *testing.T) {
	condition := &dbCore.BinaryCondition{
		Left:     dbCore.Raw("COUNT(*)"),
		Operator: ">",
		Right:    5,
	}
	builder := NewBuilder().Having(condition)
	q := builder.Build()

	assert.NotNil(t, q.Having)
	assert.Equal(t, condition, q.Having.Condition)
}

func TestBuilder_LimitOffset(t *testing.T) {
	builder := NewBuilder().Limit(10).Offset(20)
	q := builder.Build()

	assert.NotNil(t, q.Limit)
	assert.Equal(t, 10, *q.Limit)
	assert.NotNil(t, q.Offset)
	assert.Equal(t, 20, *q.Offset)
}

func TestBuilder_WithCTE(t *testing.T) {
	cteQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder := NewBuilder().WithCTE("user_cte", cteQuery)
	q := builder.Build()

	assert.Len(t, q.CTEs, 1)
	assert.Equal(t, "user_cte", q.CTEs[0].Name)
	assert.Equal(t, cteQuery, q.CTEs[0].Query)
}

func TestBuilder_Union(t *testing.T) {
	unionQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder := NewBuilder().Union(unionQuery)
	q := builder.Build()

	assert.Len(t, q.Unions, 1)
	assert.Equal(t, unionQuery, q.Unions[0].Query)
	assert.False(t, q.Unions[0].All)
}

func TestBuilder_UnionAll(t *testing.T) {
	unionQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder := NewBuilder().UnionAll(unionQuery)
	q := builder.Build()

	assert.Len(t, q.Unions, 1)
	assert.Equal(t, unionQuery, q.Unions[0].Query)
	assert.True(t, q.Unions[0].All)
}

func TestBuilder_ToSQL(t *testing.T) {
	tests := []struct {
		name     string
		builder  func() *Builder
		expected struct {
			sql  string
			args []interface{}
		}
	}{
		{
			name: "Simple SELECT",
			builder: func() *Builder {
				b := NewBuilder().
					Select("id", "name").
					From("users")
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT id, name FROM users",
				args: nil,
			},
		},
		{
			name: "SELECT with WHERE condition",
			builder: func() *Builder {
				b := NewBuilder().
					Select("id", "name").
					From("users").
					Where(&dbCore.BinaryCondition{
						Left:     "age",
						Operator: ">",
						Right:    18,
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT id, name FROM users WHERE age > ?",
				args: []interface{}{18},
			},
		},
		{
			name: "SELECT with JOIN and conditions",
			builder: func() *Builder {
				b := NewBuilder().
					Select("users.id", "users.name", "orders.id").
					From("users").
					InnerJoin("orders", &dbCore.RawCondition{
						SQL: "users.id = orders.user_id",
					}).
					Where(&dbCore.BinaryCondition{
						Left:     "users.status",
						Operator: "=",
						Right:    "active",
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT users.id, users.name, orders.id FROM users INNER JOIN orders ON users.id = orders.user_id WHERE users.status = ?",
				args: []interface{}{"active"},
			},
		},
		{
			name: "SELECT with GROUP BY and HAVING",
			builder: func() *Builder {
				b := NewBuilder().
					Select("user_id", "COUNT(*) as order_count").
					From("orders").
					GroupBy("user_id").
					Having(&dbCore.BinaryCondition{
						Left:     dbCore.Raw("COUNT(*)"),
						Operator: ">",
						Right:    5,
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT user_id, COUNT(*) as order_count FROM orders GROUP BY user_id HAVING COUNT(*) > ?",
				args: []interface{}{5},
			},
		},
		{
			name: "SELECT with IN condition",
			builder: func() *Builder {
				b := NewBuilder().
					Select("id", "name").
					From("users").
					Where(&dbCore.InCondition{
						Field:  "status",
						Values: []interface{}{"active", "pending"},
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT id, name FROM users WHERE status IN (?, ?)",
				args: []interface{}{"active", "pending"},
			},
		},
		{
			name: "SELECT with BETWEEN condition",
			builder: func() *Builder {
				b := NewBuilder().
					Select("id", "name").
					From("users").
					Where(&dbCore.BetweenCondition{
						Field: "age",
						Lower: 18,
						Upper: 65,
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT id, name FROM users WHERE age BETWEEN ? AND ?",
				args: []interface{}{18, 65},
			},
		},
		{
			name: "SELECT with subquery",
			builder: func() *Builder {
				subquery := NewBuilder().
					Select("user_id").
					From("orders").
					Where(&dbCore.BinaryCondition{
						Left:     "status",
						Operator: "=",
						Right:    "completed",
					}).
					Build()

				b := NewBuilder().Select("id", "name").From("users").Where(&dbCore.InCondition{
					Field:      "id",
					IsSubquery: true,
					Subquery:   subquery,
				})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "SELECT id, name FROM users WHERE id IN (SELECT user_id FROM orders WHERE status = ?)",
				args: []interface{}{"completed"},
			},
		},
		{
			name: "SELECT with CTE",
			builder: func() *Builder {
				cteQuery := NewBuilder().
					Select("user_id", "COUNT(*) as order_count").
					From("orders").
					GroupBy("user_id").
					Build()

				b := NewBuilder().
					WithCTE("user_orders", cteQuery).
					Select("u.id", "u.name", "uo.order_count").
					From("users u").
					InnerJoin("user_orders uo", &dbCore.RawCondition{
						SQL: "u.id = uo.user_id",
					})
				return b.(*Builder)
			},
			expected: struct {
				sql  string
				args []interface{}
			}{
				sql:  "WITH user_orders AS (SELECT user_id, COUNT(*) as order_count FROM orders GROUP BY user_id) SELECT u.id, u.name, uo.order_count FROM users u INNER JOIN user_orders uo ON u.id = uo.user_id",
				args: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := tt.builder()
			sql, args, err := builder.ToSQL()
			require.NoError(t, err)

			assert.Equal(t, tt.expected.sql, sql)
			assert.Equal(t, tt.expected.args, args)
		})
	}
}

func TestBuilder_ConditionHelpers(t *testing.T) {
	builder := NewBuilder()

	tests := []struct {
		name      string
		condition func() dbCore.IQueryBuilder
		expected  dbCore.Condition
	}{
		{
			name: "Eq",
			condition: func() dbCore.IQueryBuilder {
				return builder.Eq("id", 1)
			},
			expected: &dbCore.BinaryCondition{
				Left:     "id",
				Operator: "=",
				Right:    1,
			},
		},
		{
			name: "Neq",
			condition: func() dbCore.IQueryBuilder {
				return builder.Neq("id", 1)
			},
			expected: &dbCore.BinaryCondition{
				Left:     "id",
				Operator: "!=",
				Right:    1,
			},
		},
		{
			name: "Gt",
			condition: func() dbCore.IQueryBuilder {
				return builder.Gt("age", 18)
			},
			expected: &dbCore.BinaryCondition{
				Left:     "age",
				Operator: ">",
				Right:    18,
			},
		},
		{
			name: "In",
			condition: func() dbCore.IQueryBuilder {
				return builder.In("status", "active", "pending")
			},
			expected: &dbCore.InCondition{
				Field:  "status",
				Values: []interface{}{"active", "pending"},
			},
		},
		{
			name: "Like",
			condition: func() dbCore.IQueryBuilder {
				return builder.Like("name", "%john%")
			},
			expected: &dbCore.LikeCondition{
				Field:   "name",
				Pattern: "%john%",
			},
		},
		{
			name: "IsNull",
			condition: func() dbCore.IQueryBuilder {
				return builder.IsNull("deleted_at")
			},
			expected: &dbCore.IsNullCondition{
				Field: "deleted_at",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder = NewBuilder()
			q := tt.condition().Build()
			assert.NotNil(t, q.Where)
			assert.Len(t, q.Where.Conditions, 1)
			assert.Equal(t, tt.expected, q.Where.Conditions[0])
		})
	}
}

func TestBuilder_Subquery(t *testing.T) {
	subquery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder := NewBuilder().Subquery(subquery, "u")
	q := builder.Build()

	assert.NotNil(t, q.From)
	assert.True(t, q.From.IsSubquery)
	assert.Equal(t, subquery, q.From.Subquery)
	assert.Equal(t, "u", q.From.Alias)
}

func TestBuilder_RawCondition(t *testing.T) {
	rawCondition := &dbCore.RawCondition{
		SQL: "EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id)",
	}
	builder := NewBuilder().Where(rawCondition)
	q := builder.Build()

	assert.NotNil(t, q.Where)
	assert.Len(t, q.Where.Conditions, 1)
	assert.Equal(t, rawCondition, q.Where.Conditions[0])
}

func TestBuilder_RawSQL(t *testing.T) {
	builder := NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.RawCondition{
			SQL: "age > 18 AND status = 'active'",
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name FROM users WHERE age > 18 AND status = 'active'", sql)
	assert.Nil(t, args)
}

func TestBuilder_ToSQL_PreservesNestedConditionGrouping(t *testing.T) {
	builder := NewBuilder().
		Select("id").
		From("users").
		Where(&dbCore.CompositeCondition{
			Operator: "AND",
			Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{
					Left:     "user_id",
					Operator: "=",
					Right:    1,
				},
				&dbCore.CompositeCondition{
					Operator: "OR",
					Conditions: []dbCore.Condition{
						&dbCore.BinaryCondition{
							Left:     "status",
							Operator: "=",
							Right:    "confirmed",
						},
						&dbCore.BinaryCondition{
							Left:     "date",
							Operator: ">",
							Right:    "2026-01-01",
						},
					},
				},
			},
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)

	assert.Equal(t, "SELECT id FROM users WHERE (user_id = ? AND (status = ? OR date > ?))", sql)
	assert.Equal(t, []interface{}{1, "confirmed", "2026-01-01"}, args)
}

func TestBuilder_ComplexSubquery(t *testing.T) {
	// Create a subquery for users with active orders
	subquery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"user_id"},
		},
		From: &dbCore.FromClause{
			Table: "orders",
		},
		Where: &dbCore.WhereClause{
			Operator: "AND",
			Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{
					Left:     "status",
					Operator: "=",
					Right:    "active",
				},
			},
		},
		GroupBy: &dbCore.GroupByClause{
			Fields: []string{"user_id"},
		},
		Having: &dbCore.HavingClause{
			Condition: &dbCore.BinaryCondition{
				Left:     dbCore.Raw("COUNT(*)"),
				Operator: ">",
				Right:    5,
			},
		},
	}

	// Main query using the subquery
	builder := NewBuilder().
		Select("id", "name", "email").
		From("users").
		Where(&dbCore.InCondition{
			Field:      "id",
			IsSubquery: true,
			Subquery:   subquery,
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	expected := "SELECT id, name, email FROM users WHERE id IN (SELECT user_id FROM orders WHERE status = ? GROUP BY user_id HAVING COUNT(*) > ?)"
	assert.Equal(t, expected, sql)
	assert.Equal(t, []interface{}{"active", 5}, args)
}

func TestBuilder_NestedSubqueries(t *testing.T) {
	// Innermost subquery
	innerSubquery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"product_id"},
		},
		From: &dbCore.FromClause{
			Table: "order_items",
		},
		Where: &dbCore.WhereClause{
			Operator: "AND",
			Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{
					Left:     "quantity",
					Operator: ">",
					Right:    10,
				},
			},
		},
	}

	// Middle subquery
	middleSubquery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id"},
		},
		From: &dbCore.FromClause{
			Table: "orders",
		},
		Where: &dbCore.WhereClause{
			Operator: "AND",
			Conditions: []dbCore.Condition{
				&dbCore.InCondition{
					Field:      "id",
					IsSubquery: true,
					Subquery:   innerSubquery,
				},
			},
		},
	}

	// Main query
	builder := NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.InCondition{
			Field:      "id",
			IsSubquery: true,
			Subquery:   middleSubquery,
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	expected := "SELECT id, name FROM users WHERE id IN (SELECT id FROM orders WHERE id IN (SELECT product_id FROM order_items WHERE quantity > ?))"
	assert.Equal(t, expected, sql)
	assert.Equal(t, []interface{}{10}, args)
}

func TestBuilder_RawWithParameters(t *testing.T) {
	builder := NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.RawCondition{
			SQL:  "age > ? AND status = ?",
			Args: []interface{}{18, "active"},
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name FROM users WHERE age > ? AND status = ?", sql)
	assert.Equal(t, []interface{}{18, "active"}, args)
}

func TestBuilder_ComplexRawCondition(t *testing.T) {
	builder := NewBuilder().
		Select("id", "name", "email").
		From("users").
		Where(&dbCore.RawCondition{
			SQL: "EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id AND orders.status = 'active') AND (age > 18 OR (age = 18 AND parent_consent = true))",
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	expected := "SELECT id, name, email FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id AND orders.status = 'active') AND (age > 18 OR (age = 18 AND parent_consent = true))"
	assert.Equal(t, expected, sql)
	assert.Nil(t, args)
}

func TestBuilder_ColumnComparison(t *testing.T) {
	// Test standard condition (column to value)
	builder := NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.BinaryCondition{
			Left:     "age",
			Operator: ">",
			Right:    18,
		})
	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name FROM users WHERE age > ?", sql)
	assert.Equal(t, []interface{}{18}, args)

	// Test raw condition (column to column)
	builder = NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.RawCondition{
			SQL: "age > min_age",
		})
	sql, args, err = builder.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name FROM users WHERE age > min_age", sql)
	assert.Nil(t, args)

	// Test complex raw condition with multiple column comparisons
	builder = NewBuilder().
		Select("id", "name").
		From("users").
		Where(&dbCore.RawCondition{
			SQL: "age > min_age AND (max_age IS NULL OR age < max_age)",
		})
	sql, args, err = builder.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name FROM users WHERE age > min_age AND (max_age IS NULL OR age < max_age)", sql)
	assert.Nil(t, args)
}

func TestBuilder_RawCondition_IdentifierPlaceholders_InJoin(t *testing.T) {
	// Emulate many2many JOIN ON condition using core.RawCondition with identifier placeholders
	relatedTable := "treatment_categories"
	joinTable := "treatment_treatment_category"

	builder := NewBuilder().
		Select(relatedTable+".*").
		From(relatedTable).
		InnerJoin(joinTable, &dbCore.RawCondition{
			SQL:  "?.id = ?.?",
			Args: []interface{}{relatedTable, joinTable, "treatment_category_id"},
		}).
		Where(&dbCore.BinaryCondition{
			Left:     joinTable + "." + "treatment_id",
			Operator: "=",
			Right:    1,
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	expectedSQL := "SELECT " + relatedTable + ".* FROM " + relatedTable + " INNER JOIN " + joinTable + " ON " + relatedTable + ".id = " + joinTable + ".treatment_category_id WHERE " + joinTable + ".treatment_id = ?"
	assert.Equal(t, expectedSQL, sql)
	assert.Equal(t, []interface{}{1}, args)
}

func TestBuilder_RawCondition_MixedIdentifierAndValuePlaceholders(t *testing.T) {
	// Ensure that identifier placeholder and value placeholder coexist correctly
	table := "users"
	builder := NewBuilder().
		Select("id", "name").
		From(table).
		Where(&dbCore.RawCondition{
			SQL:  "?.name = ?",
			Args: []interface{}{table, "John"},
		})

	sql, args, err := builder.ToSQL()
	require.NoError(t, err)
	expectedSQL := "SELECT id, name FROM " + table + " WHERE " + table + ".name = ?"
	assert.Equal(t, expectedSQL, sql)
	assert.Equal(t, []interface{}{"John"}, args)
}
