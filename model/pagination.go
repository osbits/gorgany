package model

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"gorm.io/gorm/schema"
	"strconv"
	"strings"
	"time"
)

// DomainFilters is used to domain`s filter, it`s validated according fields in domain
type DomainFilters[T any] []*DomainFilter[T]

func (thiz DomainFilters[T]) GetFilters() []Filter {
	filters := make([]Filter, 0)
	for _, filter := range thiz {
		filters = append(filters, *filter.Filter)
	}
	return filters
}

type DomainFilter[T any] struct {
	Filter *Filter
}

func (thiz *DomainFilter[T]) FromMap(params map[string]string) error {
	var domain T
	filter, err := NewFilter(params["field"], params["operator"], params["value"], domain)
	if err != nil {
		return err
	}

	thiz.Filter = filter
	return nil
}

func (thiz *DomainFilter[T]) GetValue() any {
	return thiz.Filter.GetValue()
}

type Filter struct {
	Field    string
	Operator string
	Value    any
}

// Query should look like this sort[0][field]=Email&sort[0][order]=desc&sort[1][field]=Id&sort[1][order]=asc
func NewFilter(field string, operator string, value string, domain any) (*Filter, error) {
	if field == "" {
		return nil, fmt.Errorf("Filter: Field is required")
	}

	if operator == "" || (operator != "=" && operator != "!=" && operator != "like" && operator != "not like" &&
		operator != "in" && operator != "not in" && operator != ">" && operator != ">=" && operator != "<" && operator != "<=") {
		operator = "="
	}

	sc := cache.GetDomainSchemeCache().ParseDomain(domain)
	if sc == nil {
		return nil, nil
	}

	var filterValue any

	f := sc.LookUpField(field)
	if f == nil {
		return nil, fmt.Errorf("Filter: Field %s does not exist", field)
	}

	switch f.DataType {
	case schema.Time:
		if v, err := time.Parse(core.GlobalDateFormat, value); err == nil {
			filterValue = v
			break
		}
		if v, err := time.Parse(core.GlobalDateTimeFormat, value); err == nil {
			filterValue = v
			break
		}
		return nil, fmt.Errorf("Filter: Field(%s) is date format, but value not in %s or %s formats", field, core.GlobalDateFormat, core.GlobalDateTimeFormat)
	case schema.Int:
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err2.ValidationError{
				Field: "filter." + field,
				Err:   "Field is integer, but value(%s) is not integer",
			}
		}
		filterValue = v
	case schema.Bool:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("Filter: Field(%s) is bool, but value(%s) is not bool", field, value)
		}
		filterValue = v
	case schema.Float:
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("Filter: Field(%s) is float, but value(%s) is not float", field, value)
		}
		filterValue = v
	case schema.Uint:
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("Filter: Field(%s) is uint, but value(%s) is not uint", field, value)
		}
		filterValue = v
	default:
		filterValue = value
	}

	return &Filter{
		Field:    field,
		Operator: operator,
		Value:    filterValue,
	}, nil
}

func (thiz Filter) GetValue() any {
	if thiz.Operator == "in" || thiz.Operator == "not in" {
		if values, ok := thiz.Value.(string); ok {
			values := strings.Split(values, ",")
			return values
		} else {
			return nil
		}
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
	if pageSize == 0 {
		pageSize = 50 //todo
	}
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
