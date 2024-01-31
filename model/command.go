package model

import "strings"

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

func (thiz PaginationCommand) GetSort() []SortParam {
	return thiz.Sort
}

type LimitedFieldsCommand struct {
	Fields string
}

func (thiz LimitedFieldsCommand) GetFields() []string {
	return strings.Split(thiz.Fields, ",")
}
