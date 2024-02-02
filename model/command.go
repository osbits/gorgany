package model

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"strings"
)

type PaginationCommand struct {
	Page  int
	Limit int
	Sort  []SortParam
}

func (thiz PaginationCommand) GetPage() int {
	return thiz.Page
}

func (thiz PaginationCommand) GetLimit() int {
	return thiz.Limit
}

func (thiz PaginationCommand) GetSort() []core.ISortParam {
	iSortParams := make([]core.ISortParam, 0)
	for i := range thiz.Sort {
		iSortParams = append(iSortParams, thiz.Sort[i])
	}
	return iSortParams
}

type LimitedFieldsCommand struct {
	Fields string
}

func (thiz LimitedFieldsCommand) GetFields() []string {
	return strings.Split(thiz.Fields, ",")
}

func (thiz LimitedFieldsCommand) ContentType() core.ContentType {
	return core.Query
}
