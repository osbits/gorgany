package model

import (
	"github.com/osbits/gorgany/app/core"
	"strings"
)

type PaginationCommand struct {
	Page  int
	Limit int
	Sort  []SortParam
}

func (thiz PaginationCommand) GetPage() int {
	if thiz.Page == 0 {
		thiz.Page = 1
	}
	return thiz.Page
}

func (thiz PaginationCommand) GetLimit() int {
	if thiz.Limit == 0 {
		return 50 //todo read from config
	}
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
