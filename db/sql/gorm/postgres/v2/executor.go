package v2

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/db/sql/core"
	"strings"

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

// Execute executes a query without returning results
func (e *Executor) Execute(ctx context.Context, query *core.Query) error {
	gormQuery := e.buildGormQuery(query)
	return gormQuery.Exec("").Error
}

// ExecuteWithResult executes a query and stores the results in the provided destination
func (e *Executor) ExecuteWithResult(ctx context.Context, query *core.Query, result interface{}) error {
	gormQuery := e.buildGormQuery(query)
	return gormQuery.Find(result).Error
}

// Count executes a COUNT query
func (e *Executor) Count(ctx context.Context, query *core.Query) (int64, error) {
	var count int64
	gormQuery := e.buildGormQuery(query)
	err := gormQuery.Count(&count).Error
	return count, err
}

// ExecuteRaw executes a raw SQL query without returning results
func (e *Executor) ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error {
	return e.db.Raw(sql, args...).Exec("").Error
}

// ExecuteRawWithResult executes a raw SQL query and stores the results in the provided destination
func (e *Executor) ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error {
	return e.db.Raw(sql, args...).Scan(result).Error
}

// CountRaw executes a raw SQL COUNT query
func (e *Executor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	var count int64
	err := e.db.Raw(sql, args...).Count(&count).Error
	return count, err
}

// buildGormQuery converts our Query to a GORM query
func (e *Executor) buildGormQuery(query *core.Query) *gorm.DB {
	gormQuery := e.db

	// Add CTEs if any
	if len(query.CTEs) > 0 {
		cteParts := make([]string, len(query.CTEs))
		for i, cte := range query.CTEs {
			cteParts[i] = fmt.Sprintf("%s AS (%s)", cte.Name, cte.Query)
		}
		gormQuery = gormQuery.Raw("WITH " + strings.Join(cteParts, ", "))
	}

	// Add SELECT clause
	if query.Select != nil {
		var selectClause string
		if query.Select.Distinct {
			if len(query.Select.DistinctOn) > 0 {
				selectClause = fmt.Sprintf("SELECT DISTINCT ON (%s) %s",
					strings.Join(query.Select.DistinctOn, ", "),
					strings.Join(query.Select.Fields, ", "))
			} else {
				selectClause = fmt.Sprintf("SELECT DISTINCT %s",
					strings.Join(query.Select.Fields, ", "))
			}
		} else {
			selectClause = fmt.Sprintf("SELECT %s",
				strings.Join(query.Select.Fields, ", "))
		}
		gormQuery = gormQuery.Raw(selectClause)
	}

	// Add FROM clause
	if query.From != nil {
		if query.From.IsSubquery {
			gormQuery = gormQuery.Raw(fmt.Sprintf("FROM (%s) AS %s",
				query.From.Subquery,
				query.From.Alias))
		} else {
			gormQuery = gormQuery.Raw(fmt.Sprintf("FROM %s",
				query.From.Table))
		}
	}

	// Add JOINs
	for _, join := range query.Joins {
		var joinClause string
		if join.IsSubquery {
			joinClause = fmt.Sprintf("%s JOIN (%s) AS %s",
				join.Type,
				join.Subquery,
				join.Alias)
		} else {
			joinClause = fmt.Sprintf("%s JOIN %s",
				join.Type,
				join.Table)
		}

		if join.Condition != nil {
			sql, _ := join.Condition.ToSQL()
			joinClause += fmt.Sprintf(" ON %s", sql)
		}

		gormQuery = gormQuery.Raw(joinClause)
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, _ := query.Where.ToSQL()
		gormQuery = gormQuery.Raw(fmt.Sprintf("WHERE %s", whereSQL))
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		gormQuery = gormQuery.Raw(fmt.Sprintf("GROUP BY %s",
			strings.Join(query.GroupBy.Fields, ", ")))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, _ := query.Having.ToSQL()
		gormQuery = gormQuery.Raw(fmt.Sprintf("HAVING %s", havingSQL))
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		orderByParts := make([]string, len(query.OrderBy.Fields))
		for i, field := range query.OrderBy.Fields {
			if field.Direction != "" {
				orderByParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
			} else {
				orderByParts[i] = field.Field
			}
		}
		gormQuery = gormQuery.Raw(fmt.Sprintf("ORDER BY %s",
			strings.Join(orderByParts, ", ")))
	}

	// Add LIMIT clause
	if query.Limit != nil {
		gormQuery = gormQuery.Raw(fmt.Sprintf("LIMIT %d", *query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		gormQuery = gormQuery.Raw(fmt.Sprintf("OFFSET %d", *query.Offset))
	}

	// Add UNION clauses
	for _, union := range query.Unions {
		if union.All {
			gormQuery = gormQuery.Raw(fmt.Sprintf("UNION ALL %s", union.Query))
		} else {
			gormQuery = gormQuery.Raw(fmt.Sprintf("UNION %s", union.Query))
		}
	}

	// Add RETURNING clause
	if len(query.Returning) > 0 {
		gormQuery = gormQuery.Raw(fmt.Sprintf("RETURNING %s",
			strings.Join(query.Returning, ", ")))
	}

	return gormQuery
}
