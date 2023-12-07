package postgres

import (
	"fmt"
	"gorgany/app/core"
	"strings"
)

type Having struct {
	havingItems []HavingItem
}

func (thiz *Having) AddItem(rawStatement string, operator string, value any) {
	thiz.havingItems = append(thiz.havingItems, HavingItem{
		statement: rawStatement,
		operator:  operator,
		value:     value,
	})
}

func (thiz *Having) ToQuery() (string, []any) {
	havingItems := make([]string, 0)
	allArgs := make([]any, 0)
	for _, item := range thiz.havingItems {
		preparedForSql, args := item.PrepareForSql()
		havingItems = append(havingItems, preparedForSql)
		allArgs = append(allArgs, args...)
	}
	return strings.Join(havingItems, ","), allArgs
}

type HavingItem struct {
	statement string
	operator  string
	value     any
}

func (thiz HavingItem) PrepareForSql() (string, []any) {
	switch thiz.value.(type) {
	case *Raw:
		raw := thiz.value.(*Raw)
		return fmt.Sprintf("%s %s (%s)", thiz.statement, thiz.operator, raw.sql), raw.args
	case core.IQueryBuilder:
		builder := thiz.value.(core.IQueryBuilder)
		query, args := builder.ToQuery()
		return fmt.Sprintf("%s %s (%s)", thiz.statement, thiz.operator, query), args
	default:
		return fmt.Sprintf("%s %s ?", thiz.statement, thiz.operator), []any{thiz.value}
	}
}
