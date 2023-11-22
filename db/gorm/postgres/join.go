package postgres

import (
	"fmt"
	"gorgany/app/core"
	"strings"
)

type Join struct {
	joinItems map[string][]JoinItems
}

func (thiz *Join) InnerJoin(table any, left, operator, right string) {
	thiz.joinItems["INNER JOIN"] = append(thiz.joinItems["INNER JOIN"], JoinItems{
		table:    table,
		leftOn:   left,
		rightOn:  right,
		operator: operator,
	})
}

func (thiz *Join) LeftJoin(table any, right, operator, left string) {
	thiz.joinItems["LEFT JOIN"] = append(thiz.joinItems["LEFT JOIN"], JoinItems{
		table:    table,
		leftOn:   left,
		rightOn:  right,
		operator: operator,
	})
}

func (thiz *Join) RightJoin(table any, right, operator, left string) {
	thiz.joinItems["RIGHT JOIN"] = append(thiz.joinItems["RIGHT JOIN"], JoinItems{
		table:    table,
		leftOn:   left,
		rightOn:  right,
		operator: operator,
	})
}

func (thiz *Join) FullJoin(table any, right, operator, left string) {
	thiz.joinItems["FULL JOIN"] = append(thiz.joinItems["FULL JOIN"], JoinItems{
		table:    table,
		leftOn:   left,
		rightOn:  right,
		operator: operator,
	})
}

func (thiz *Join) ToQuery() (string, []any) {
	allJoins := make([]string, 0)
	allArgs := make([]any, 0)

	for joinType, items := range thiz.joinItems {
		joins := make([]string, 0)
		for _, item := range items {
			sql, args := item.PrepareForSQL()
			joins = append(joins, sql)
			allArgs = append(allArgs, args)
		}
		allJoins = append(allJoins, fmt.Sprintf("%s %s", joinType, strings.Join(joins, ", ")))
	}
	return strings.Join(allJoins, " "), allArgs
}

type JoinItems struct {
	table    any
	leftOn   string
	rightOn  string
	operator string
}

func (thiz JoinItems) PrepareForSQL() (string, []any) {
	switch thiz.table.(type) {
	case *Raw:
		raw := thiz.table.(*Raw)
		return fmt.Sprintf("(%s) %s ON %s %s %s", raw.sql, raw.tableAlias, thiz.leftOn, thiz.operator, thiz.rightOn), raw.args
	case core.IQueryBuilder:
		builder := thiz.table.(core.IQueryBuilder)
		query, args := builder.ToQuery()
		builder.GetWhere()
		return fmt.Sprintf("(%s) %s ON %s %s %s", query, builder.GetAlias(), thiz.leftOn, thiz.operator, thiz.rightOn), args
	default:
		table := thiz.table.(string)
		return fmt.Sprintf("%s ON %s %s %s", table, thiz.leftOn, thiz.operator, thiz.rightOn), []any{}
	}
}
