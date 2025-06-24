package v2

import (
	"context"
	"fmt"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/db/sql/core"

	"gorm.io/gorm"
)

// Executor implements the QueryExecutor interface
type Executor struct {
	db *gorm.DB
}

// NewExecutor creates a new query executor
func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{db: db}
}

// Exec executes a query without returning results
func (e *Executor) Exec(ctx context.Context, query *core.Query) error {
	sql, args := e.BuildSQL(query)
	return e.db.Exec(sql, args...).Error
}

// QueryOne executes a query and stores the result in the provided destination
func (e *Executor) QueryOne(ctx context.Context, query *core.Query, result interface{}) error {
	sql, args := e.BuildSQL(query)
	return e.db.Raw(sql, args...).Scan(result).Error
}

// QueryList executes a query and stores the results in the provided destination
func (e *Executor) QueryList(ctx context.Context, query *core.Query, result interface{}) error {
	sql, args := e.BuildSQL(query)
	return e.db.Raw(sql, args...).Scan(result).Error
}

// Count executes a COUNT query
func (e *Executor) Count(ctx context.Context, query *core.Query) (int64, error) {
	sql, args := e.BuildSQL(query)
	var count int64
	err := e.db.Raw(sql, args...).Count(&count).Error
	return count, err
}

// ExecRaw executes a raw SQL query without returning results
func (e *Executor) ExecRaw(ctx context.Context, sql string, args ...interface{}) error {
	return e.db.Exec(sql, args...).Error
}

// QueryRaw executes a raw SQL query and stores the results in the provided destination
func (e *Executor) QueryRaw(ctx context.Context, result interface{}, sql string, args ...interface{}) core.QueryResult {
	// Check if result is nil
	if result == nil {
		return core.QueryResult{Error: fmt.Errorf("destination cannot be nil")}
	}

	db := e.db.Raw(sql, args...)
	err := db.Scan(result).Error

	// Create query result metadata
	queryResult := core.QueryResult{
		Error:        err,
		RowsAffected: db.RowsAffected,
		Found:        db.RowsAffected > 0,
	}

	return queryResult
}

// CountRaw executes a raw SQL COUNT query
func (e *Executor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	var count int64
	err := e.db.Raw(sql, args...).Count(&count).Error
	return count, err
}

// BuildSQL converts a query to SQL and returns both the SQL string and arguments
func (e *Executor) BuildSQL(q *core.Query) (string, []interface{}) {
	builder := NewBuilder()
	builder.query = q
	return builder.ToSQL()
}

// buildGormQuery converts our Query to a GORM query
func (e *Executor) buildGormQuery(query *core.Query) *gorm.DB {
	var sqlBuilder strings.Builder
	var args []interface{}
	argIndex := 1

	// Add CTEs if any
	if len(query.CTEs) > 0 {
		sqlBuilder.WriteString("WITH ")
		cteParts := make([]string, len(query.CTEs))
		for i, cte := range query.CTEs {
			cteSQL, cteArgs := buildSubquery(cte.Query, &argIndex)
			cteParts[i] = fmt.Sprintf("%s AS (%s)", cte.Name, cteSQL)
			args = append(args, cteArgs...)
		}
		sqlBuilder.WriteString(strings.Join(cteParts, ", "))
		sqlBuilder.WriteString(" ")
	}

	// Add SELECT clause
	if query.Select != nil {
		if query.Select.Distinct {
			if len(query.Select.DistinctOn) > 0 {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT ON (%s) %s",
					strings.Join(query.Select.DistinctOn, ", "),
					strings.Join(query.Select.Fields, ", ")))
			} else {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT %s",
					strings.Join(query.Select.Fields, ", ")))
			}
		} else {
			sqlBuilder.WriteString(fmt.Sprintf("SELECT %s",
				strings.Join(query.Select.Fields, ", ")))
		}
	}

	// Add FROM clause
	if query.From != nil {
		sqlBuilder.WriteString(" FROM ")
		if query.From.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquery(query.From.Subquery, &argIndex)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, query.From.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(query.From.Table)
		}
	}

	// Add JOINs
	for _, join := range query.Joins {
		sqlBuilder.WriteString(fmt.Sprintf(" %s JOIN ", join.Type))
		if join.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquery(join.Subquery, &argIndex)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, join.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(join.Table)
		}

		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			sqlBuilder.WriteString(fmt.Sprintf(" ON %s", conditionSQL))
			args = append(args, conditionArgs...)
		}
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs := query.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" GROUP BY %s",
			strings.Join(query.GroupBy.Fields, ", ")))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs := query.Having.ToSQL()
		if havingSQL != "" {
			sqlBuilder.WriteString(" HAVING ")
			sqlBuilder.WriteString(havingSQL)
			args = append(args, havingArgs...)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		sqlBuilder.WriteString(" ORDER BY ")
		orderByParts := make([]string, len(query.OrderBy.Fields))
		for i, field := range query.OrderBy.Fields {
			if field.Direction != "" {
				orderByParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
			} else {
				orderByParts[i] = field.Field
			}
		}
		sqlBuilder.WriteString(strings.Join(orderByParts, ", "))
	}

	// Add LIMIT clause
	if query.Limit != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	// Add UNION clauses
	for _, union := range query.Unions {
		if union.All {
			sqlBuilder.WriteString(" UNION ALL ")
		} else {
			sqlBuilder.WriteString(" UNION ")
		}
		unionSQL, unionArgs := buildSubquery(union.Query, &argIndex)
		sqlBuilder.WriteString(unionSQL)
		args = append(args, unionArgs...)
	}

	// Add RETURNING clause
	if len(query.Returning) > 0 {
		sqlBuilder.WriteString(fmt.Sprintf(" RETURNING %s",
			strings.Join(query.Returning, ", ")))
	}

	return e.db.Raw(sqlBuilder.String(), args...)
}

// buildSubquery builds a subquery and returns its SQL and arguments
func buildSubquery(query *core.Query, argIndex *int) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	// Add SELECT clause
	if query.Select != nil {
		if query.Select.Distinct {
			if len(query.Select.DistinctOn) > 0 {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT ON (%s) %s",
					strings.Join(query.Select.DistinctOn, ", "),
					strings.Join(query.Select.Fields, ", ")))
			} else {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT %s",
					strings.Join(query.Select.Fields, ", ")))
			}
		} else {
			sqlBuilder.WriteString(fmt.Sprintf("SELECT %s",
				strings.Join(query.Select.Fields, ", ")))
		}
	}

	// Add FROM clause
	if query.From != nil {
		sqlBuilder.WriteString(" FROM ")
		if query.From.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquery(query.From.Subquery, argIndex)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, query.From.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(query.From.Table)
		}
	}

	// Add JOINs
	for _, join := range query.Joins {
		sqlBuilder.WriteString(fmt.Sprintf(" %s JOIN ", join.Type))
		if join.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquery(join.Subquery, argIndex)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, join.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(join.Table)
		}

		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			sqlBuilder.WriteString(fmt.Sprintf(" ON %s", conditionSQL))
			args = append(args, conditionArgs...)
		}
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs := query.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" GROUP BY %s",
			strings.Join(query.GroupBy.Fields, ", ")))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs := query.Having.ToSQL()
		if havingSQL != "" {
			sqlBuilder.WriteString(" HAVING ")
			sqlBuilder.WriteString(havingSQL)
			args = append(args, havingArgs...)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		sqlBuilder.WriteString(" ORDER BY ")
		orderByParts := make([]string, len(query.OrderBy.Fields))
		for i, field := range query.OrderBy.Fields {
			if field.Direction != "" {
				orderByParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
			} else {
				orderByParts[i] = field.Field
			}
		}
		sqlBuilder.WriteString(strings.Join(orderByParts, ", "))
	}

	// Add LIMIT clause
	if query.Limit != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	return sqlBuilder.String(), args
}
