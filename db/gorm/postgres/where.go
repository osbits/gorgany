package postgres

import (
	"fmt"
	"gorgany/app/core"
	"strings"
)

type Where struct {
	operator   string // and/or
	whereItems []WhereItem
}

func (thiz *Where) AddCondition(column string, operator string, value any) {
	thiz.whereItems = append(thiz.whereItems, WhereItem{
		column:   column,
		operator: operator,
		value:    value,
	})
}

func (thiz *Where) AddNestedCondition(connectorOperator string, nestedWhere core.IWhere) {
	where := nestedWhere.(*Where)
	where.operator = connectorOperator
	thiz.whereItems = append(thiz.whereItems, WhereItem{complexWhere: where})
}

func (thiz *Where) ToQuery() (string, []any) {
	whereItems := make([]string, 0)
	allArgs := make([]any, 0)
	for _, item := range thiz.whereItems {
		preparedForSql, args := item.PrepareForSql()
		whereItems = append(whereItems, preparedForSql)
		allArgs = append(allArgs, args...)
	}
	return strings.Join(whereItems, fmt.Sprintf(" %s ", thiz.operator)), allArgs
}

type WhereItem struct {
	complexWhere core.IWhere
	column       string
	operator     string // =, !=, like, in, not in, any todo should be const
	value        any
}

func (thiz WhereItem) PrepareForSql() (string, []any) {
	if thiz.complexWhere != nil {
		sql, args := thiz.complexWhere.ToQuery()
		return fmt.Sprintf("(%s)", sql), args
	}

	return thiz.prepareValue(thiz.value)
}

func (thiz WhereItem) prepareValue(value any) (string, []any) {
	switch value.(type) {
	case *Raw:
		raw := value.(*Raw)
		return fmt.Sprintf("%s %s (%s)", thiz.column, thiz.operator, raw.sql), raw.args
	case core.IQueryBuilder:
		builder := value.(core.IQueryBuilder)
		query, args := builder.ToQuery()
		return fmt.Sprintf("%s %s (%s)", thiz.column, thiz.operator, query), args
	case Between:
		between := value.(Between)
		query, args := between.ToQuery()
		return fmt.Sprintf("%s %s %s", thiz.column, thiz.operator, query), args
	default:
		if strings.ToUpper(thiz.operator) == "IN" || strings.ToUpper(thiz.operator) == "ANY" {
			if slice, ok := value.([]any); ok {
				if len(slice) == 1 {
					return thiz.prepareValue(slice[0])
				}
			}
			return fmt.Sprintf("%s %s (?)", thiz.column, thiz.operator), []any{value}
		}
		return fmt.Sprintf("%s %s ?", thiz.column, thiz.operator), []any{value}
	}
}
