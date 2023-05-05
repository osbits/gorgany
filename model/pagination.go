package model

type PaginationParams struct {
	Page     int
	PageSize int
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
