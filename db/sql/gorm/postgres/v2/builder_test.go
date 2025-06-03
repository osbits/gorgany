package v2

import (
	"testing"

	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	"github.com/stretchr/testify/assert"
)

func TestNewBuilder(t *testing.T) {
	builder := NewBuilder()
	assert.NotNil(t, builder)
	assert.NotNil(t, builder.query)
	assert.NotNil(t, builder.dialect)
}

func TestBuilder_Select(t *testing.T) {
	builder := NewBuilder()
	builder.Select("id", "name", "email")

	assert.NotNil(t, builder.query.Select)
	assert.Equal(t, []string{"id", "name", "email"}, builder.query.Select.Fields)
}

func TestBuilder_From(t *testing.T) {
	builder := NewBuilder()
	builder.From("users")

	assert.NotNil(t, builder.query.From)
	assert.Equal(t, "users", builder.query.From.Table)
}

func TestBuilder_Where(t *testing.T) {
	builder := NewBuilder()
	condition := &dbCore.BinaryCondition{
		Left:     "id",
		Operator: "=",
		Right:    1,
	}
	builder.Where(condition)

	assert.NotNil(t, builder.query.Where)
	assert.Equal(t, "AND", builder.query.Where.Operator)
	assert.Len(t, builder.query.Where.Conditions, 1)
	assert.Equal(t, condition, builder.query.Where.Conditions[0])
}

func TestBuilder_Join(t *testing.T) {
	builder := NewBuilder()
	join := &dbCore.JoinClause{
		Type:  "INNER",
		Table: "orders",
		Condition: &dbCore.BinaryCondition{
			Left:     "users.id",
			Operator: "=",
			Right:    "orders.user_id",
		},
	}
	builder.Join(join)

	assert.Len(t, builder.query.Joins, 1)
	assert.Equal(t, join, builder.query.Joins[0])
}

func TestBuilder_OrderBy(t *testing.T) {
	builder := NewBuilder()
	builder.OrderBy("created_at", "DESC")

	assert.NotNil(t, builder.query.OrderBy)
	assert.Len(t, builder.query.OrderBy.Fields, 1)
	assert.Equal(t, "created_at", builder.query.OrderBy.Fields[0].Field)
	assert.Equal(t, "DESC", builder.query.OrderBy.Fields[0].Direction)
}

func TestBuilder_GroupBy(t *testing.T) {
	builder := NewBuilder()
	builder.GroupBy("status", "type")

	assert.NotNil(t, builder.query.GroupBy)
	assert.Equal(t, []string{"status", "type"}, builder.query.GroupBy.Fields)
}

func TestBuilder_Having(t *testing.T) {
	builder := NewBuilder()
	condition := &dbCore.BinaryCondition{
		Left:     "COUNT(*)",
		Operator: ">",
		Right:    5,
	}
	builder.Having(condition)

	assert.NotNil(t, builder.query.Having)
	assert.Equal(t, condition, builder.query.Having.Condition)
}

func TestBuilder_LimitOffset(t *testing.T) {
	builder := NewBuilder()
	builder.Limit(10)
	builder.Offset(20)

	assert.NotNil(t, builder.query.Limit)
	assert.Equal(t, 10, *builder.query.Limit)
	assert.NotNil(t, builder.query.Offset)
	assert.Equal(t, 20, *builder.query.Offset)
}

func TestBuilder_WithCTE(t *testing.T) {
	builder := NewBuilder()
	cteQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder.WithCTE("user_cte", cteQuery)

	assert.Len(t, builder.query.CTEs, 1)
	assert.Equal(t, "user_cte", builder.query.CTEs[0].Name)
	assert.Equal(t, cteQuery, builder.query.CTEs[0].Query)
}

func TestBuilder_Union(t *testing.T) {
	builder := NewBuilder()
	unionQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder.Union(unionQuery)

	assert.Len(t, builder.query.Unions, 1)
	assert.Equal(t, unionQuery, builder.query.Unions[0].Query)
	assert.False(t, builder.query.Unions[0].All)
}

func TestBuilder_UnionAll(t *testing.T) {
	builder := NewBuilder()
	unionQuery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder.UnionAll(unionQuery)

	assert.Len(t, builder.query.Unions, 1)
	assert.Equal(t, unionQuery, builder.query.Unions[0].Query)
	assert.True(t, builder.query.Unions[0].All)
}

func TestBuilder_ToSQL(t *testing.T) {
	tests := []struct {
		name     string
		builder  func() *Builder
		expected string
	}{
		{
			name: "simple select",
			builder: func() *Builder {
				b := NewBuilder()
				b.Select("id", "name")
				b.From("users")
				return b
			},
			expected: "SELECT id, name FROM users",
		},
		{
			name: "select with where",
			builder: func() *Builder {
				b := NewBuilder()
				b.Select("id", "name")
				b.From("users")
				b.Where(&dbCore.BinaryCondition{
					Left:     "id",
					Operator: "=",
					Right:    1,
				})
				return b
			},
			expected: "SELECT id, name FROM users WHERE id = ?",
		},
		{
			name: "select with join",
			builder: func() *Builder {
				b := NewBuilder()
				b.Select("users.id", "users.name", "orders.id")
				b.From("users")
				b.InnerJoin("orders", &dbCore.RawCondition{
					SQL: "users.id = orders.user_id",
				})
				return b
			},
			expected: "SELECT users.id, users.name, orders.id FROM users INNER JOIN orders ON users.id = orders.user_id",
		},
		{
			name: "select with group by and having",
			builder: func() *Builder {
				b := NewBuilder()
				b.Select("status", "COUNT(*)")
				b.From("orders")
				b.GroupBy("status")
				b.Having(&dbCore.BinaryCondition{
					Left:     "COUNT(*)",
					Operator: ">",
					Right:    5,
				})
				return b
			},
			expected: "SELECT status, COUNT(*) FROM orders GROUP BY status HAVING COUNT(*) > ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := tt.builder()
			sql := builder.ToSQL()
			assert.Equal(t, tt.expected, sql)
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
			tt.condition()
			assert.NotNil(t, builder.query.Where)
			assert.Len(t, builder.query.Where.Conditions, 1)
			assert.Equal(t, tt.expected, builder.query.Where.Conditions[0])
		})
	}
}

func TestBuilder_Subquery(t *testing.T) {
	builder := NewBuilder()
	subquery := &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields: []string{"id", "name"},
		},
		From: &dbCore.FromClause{
			Table: "users",
		},
	}
	builder.Subquery(subquery, "u")

	assert.NotNil(t, builder.query.From)
	assert.True(t, builder.query.From.IsSubquery)
	assert.Equal(t, subquery, builder.query.From.Subquery)
	assert.Equal(t, "u", builder.query.From.Alias)
}

func TestBuilder_RawCondition(t *testing.T) {
	builder := NewBuilder()
	rawCondition := &dbCore.RawCondition{
		SQL: "EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id)",
	}
	builder.Where(rawCondition)

	assert.NotNil(t, builder.query.Where)
	assert.Len(t, builder.query.Where.Conditions, 1)
	assert.Equal(t, rawCondition, builder.query.Where.Conditions[0])
}

func TestBuilder_RawSQL(t *testing.T) {
	builder := NewBuilder()
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.RawCondition{
		SQL: "age > 18 AND status = 'active'",
	})

	sql := builder.ToSQL()
	assert.Equal(t, "SELECT id, name FROM users WHERE age > 18 AND status = 'active'", sql)
}

func TestBuilder_ComplexSubquery(t *testing.T) {
	builder := NewBuilder()

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
				Left:     "COUNT(*)",
				Operator: ">",
				Right:    5,
			},
		},
	}

	// Main query using the subquery
	builder.Select("id", "name", "email")
	builder.From("users")
	builder.Where(&dbCore.InCondition{
		Field:      "id",
		IsSubquery: true,
		Subquery:   subquery,
	})

	sql := builder.ToSQL()
	expected := "SELECT id, name, email FROM users WHERE id IN (SELECT user_id FROM orders WHERE status = ? GROUP BY user_id HAVING COUNT(*) > ?)"
	assert.Equal(t, expected, sql)

	// Verify that the query has the correct arguments
	query := builder.Build()
	assert.NotNil(t, query.Where)
	assert.Len(t, query.Where.Conditions, 1)

	inCondition, ok := query.Where.Conditions[0].(*dbCore.InCondition)
	assert.True(t, ok)
	assert.NotNil(t, inCondition.Subquery)

	// Verify subquery conditions
	assert.Len(t, inCondition.Subquery.Where.Conditions, 1)
	binaryCondition, ok := inCondition.Subquery.Where.Conditions[0].(*dbCore.BinaryCondition)
	assert.True(t, ok)
	assert.Equal(t, "active", binaryCondition.Right)

	// Verify HAVING condition
	assert.NotNil(t, inCondition.Subquery.Having)
	havingBinaryCondition, ok := inCondition.Subquery.Having.Condition.(*dbCore.BinaryCondition)
	assert.True(t, ok)
	assert.Equal(t, 5, havingBinaryCondition.Right)
}

func TestBuilder_NestedSubqueries(t *testing.T) {
	builder := NewBuilder()

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
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.InCondition{
		Field:      "id",
		IsSubquery: true,
		Subquery:   middleSubquery,
	})

	sql := builder.ToSQL()
	expected := "SELECT id, name FROM users WHERE id IN (SELECT id FROM orders WHERE id IN (SELECT product_id FROM order_items WHERE quantity > ?))"
	assert.Equal(t, expected, sql)
}

func TestBuilder_RawWithParameters(t *testing.T) {
	builder := NewBuilder()
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.RawCondition{
		SQL:  "age > ? AND status = ?",
		Args: []interface{}{18, "active"},
	})

	sql := builder.ToSQL()
	// Note: In a real implementation, you would need to handle parameter binding
	// This is just a test to ensure the raw SQL is properly formatted
	assert.Equal(t, "SELECT id, name FROM users WHERE age > ? AND status = ?", sql)
}

func TestBuilder_ComplexRawCondition(t *testing.T) {
	builder := NewBuilder()
	builder.Select("id", "name", "email")
	builder.From("users")
	builder.Where(&dbCore.RawCondition{
		SQL: "EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id AND orders.status = 'active') AND (age > 18 OR (age = 18 AND parent_consent = true))",
	})

	sql := builder.ToSQL()
	expected := "SELECT id, name, email FROM users WHERE EXISTS (SELECT 1 FROM orders WHERE orders.user_id = users.id AND orders.status = 'active') AND (age > 18 OR (age = 18 AND parent_consent = true))"
	assert.Equal(t, expected, sql)
}

func TestBuilder_ColumnComparison(t *testing.T) {
	// Test standard condition (column to value)
	builder := NewBuilder()
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.BinaryCondition{
		Left:     "age",
		Operator: ">",
		Right:    18,
	})
	sql := builder.ToSQL()
	assert.Equal(t, "SELECT id, name FROM users WHERE age > ?", sql)

	// Test raw condition (column to column)
	builder = NewBuilder()
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.RawCondition{
		SQL: "age > min_age",
	})
	sql = builder.ToSQL()
	assert.Equal(t, "SELECT id, name FROM users WHERE age > min_age", sql)

	// Test complex raw condition with multiple column comparisons
	builder = NewBuilder()
	builder.Select("id", "name")
	builder.From("users")
	builder.Where(&dbCore.RawCondition{
		SQL: "age > min_age AND (max_age IS NULL OR age < max_age)",
	})
	sql = builder.ToSQL()
	assert.Equal(t, "SELECT id, name FROM users WHERE age > min_age AND (max_age IS NULL OR age < max_age)", sql)
}
