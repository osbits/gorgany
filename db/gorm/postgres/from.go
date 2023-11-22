package postgres

import (
	"fmt"
	"gorgany/app/core"
	"strings"
)

type From struct {
	fromItems []FromItem
}

func (thiz *From) From(table any, alias string) {
	thiz.fromItems = append(thiz.fromItems, FromItem{
		table: table,
		alias: alias,
	})
}

func (thiz *From) ToQuery() (string, []any) {
	froms := make([]string, 0)
	allArgs := make([]any, 0)
	for _, item := range thiz.fromItems {
		query, args := item.PrepareForSQL()
		froms = append(froms, query)
		allArgs = append(allArgs, args...)
	}
	return strings.Join(froms, ", "), allArgs
}

type FromItem struct {
	table any
	alias string
}

func (thiz FromItem) PrepareForSQL() (string, []any) {
	var tableDefinition string
	var alias string
	var args []any
	switch thiz.table.(type) {
	case *Raw:
		raw := thiz.table.(*Raw)
		tableDefinition = fmt.Sprintf("(%s)", raw.sql)
		alias = raw.tableAlias
		args = raw.args
		break
	case core.IQueryBuilder:
		builder := thiz.table.(core.IQueryBuilder)
		query, arguments := builder.ToQuery()
		builder.GetWhere()

		args = arguments

		tableDefinition = fmt.Sprintf("(%s)", query)
		alias = builder.GetAlias()
		break
	default:
		tableDefinition = thiz.table.(string)
		alias = thiz.alias
	}

	if alias != "" {
		return tableDefinition + " " + alias, args
	}
	return tableDefinition, args
}
