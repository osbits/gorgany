package model

import (
	"fmt"
	"strings"
)

type Filter struct {
	Field    string
	Operator string
	Value    string
}

// Query should look like this sort[0][field]=Email&sort[0][order]=desc&sort[1][field]=Id&sort[1][order]=asc
func NewFilter(field string, operator string, value string) (*Filter, error) {
	if field == "" {
		return nil, fmt.Errorf("Filter: Field is required")
	}

	if operator == "" || (operator != "=" && operator != "!=" && operator != "like" && operator != "not like" && operator != "in" && operator != "not in") {
		operator = "="
	}

	return &Filter{
		Field:    field,
		Operator: operator,
		Value:    value,
	}, nil
}

func (thiz Filter) GetValue() any {
	if thiz.Operator == "in" || thiz.Operator == "not in" {
		values := strings.Split(thiz.Value, ",")
		return values
	}
	return thiz.Value
}

type SortParam struct {
	Field string
	Order string
}

// Query should look like this sort[0][field]=Email&sort[0][order]=desc&sort[1][field]=Id&sort[1][order]=asc
func NewSortParam(field string, order string) (*SortParam, error) {
	if field == "" {
		return nil, fmt.Errorf("SortParam: Field is required")
	}

	if order == "" || (order != "desc" && order != "asc") {
		order = "asc"
	}

	return &SortParam{
		Field: field,
		Order: order,
	}, nil
}

func NewPaginationParams(page int, pageSize int, sort []SortParam, filters []Filter) *PaginationParams {
	return &PaginationParams{
		Page:     page,
		PageSize: pageSize,
		Sort:     sort,
		Filters:  filters,
	}
}

type PaginationParams struct {
	Page     int
	PageSize int
	Sort     []SortParam
	Filters  []Filter
}

func (thiz PaginationParams) Offset() int {
	return (thiz.Page - 1) * thiz.PageSize
}

func NewPaginatedCollection[T any](collection []T, total int, offset int, perPage int) *PaginatedCollection[T] {
	return &PaginatedCollection[T]{
		Collection: collection,
		Total:      total,
		Offset:     offset,
		PerPage:    perPage,
	}
}

type PaginatedCollection[T any] struct {
	Collection []T
	Total      int
	Offset     int
	PerPage    int
}
