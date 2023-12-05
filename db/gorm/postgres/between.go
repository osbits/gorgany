package postgres

import (
	"fmt"
	"gorgany/app/core"
)

type Between struct {
	firstValue  any
	secondValue any
}

func (thiz *Between) ToQuery() (string, []any) {
	firstPart, firstArgs := thiz.prepareValue(thiz.firstValue)
	secondPart, secondArgs := thiz.prepareValue(thiz.secondValue)
	args := append(firstArgs, secondArgs...)
	return fmt.Sprintf("%s and %s", firstPart, secondPart), args
}

func (thiz *Between) prepareValue(value any) (string, []any) {
	switch value.(type) {
	case *Raw:
		raw := value.(*Raw)
		return fmt.Sprintf("(%s)", raw.sql), raw.args
	case core.IQueryBuilder:
		builder := value.(core.IQueryBuilder)
		query, args := builder.ToQuery()
		return fmt.Sprintf("(%s)", query), args
	default:
		return "?", []any{value}
	}
}
