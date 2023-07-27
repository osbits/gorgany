package model

import "strings"

func NewPaginationParams(page int, pageSize int, sort string, order string) *PaginationParams {
	if order == "" || (order != "asc" && order != "desc") {
		order = "asc"
	}

	return &PaginationParams{
		Page:     page,
		PageSize: pageSize,
		Sort:     sort,
		Order:    strings.ToUpper(order),
	}
}

type PaginationParams struct {
	Page     int
	PageSize int
	Sort     string
	Order    string //desc, asc
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
