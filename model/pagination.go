package model

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/osbits/gorgany/app/core"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	v2 "github.com/osbits/gorgany/db/sql/gorm/postgres/v2"
	err2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/log"
	"github.com/osbits/gorgany/service/cache"
	"gorm.io/gorm/schema"
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

func (thiz *DomainFilter[T]) ValueOfMap(params map[string]string) error {
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

// FilterAccessConfig defines access control for filters
type FilterAccessConfig struct {
	AllowedFields    []string `json:"allowed_fields,omitempty"`
	AllowedOperators []string `json:"allowed_operators,omitempty"`
	MaxFilters       int      `json:"max_filters,omitempty"`
	Roles            []string `json:"roles,omitempty"`
	ForGuest         bool     `json:"for_guest,omitempty"`
}

// NewFilterAccessConfig creates a new filter access configuration
func NewFilterAccessConfig() *FilterAccessConfig {
	return &FilterAccessConfig{
		AllowedFields:    []string{},
		AllowedOperators: []string{"=", "!=", "like", "not like", "in", "not in", ">", ">=", "<", "<="},
		MaxFilters:       10,
		Roles:            []string{},
		ForGuest:         false,
	}
}

// IsFieldAllowed checks if a field is allowed for filtering
func (fac *FilterAccessConfig) IsFieldAllowed(field string) bool {
	if len(fac.AllowedFields) == 0 {
		return true // If no fields specified, all fields are allowed
	}

	for _, allowedField := range fac.AllowedFields {
		if allowedField == field {
			return true
		}
	}
	return false
}

// IsOperatorAllowed checks if an operator is allowed for filtering
func (fac *FilterAccessConfig) IsOperatorAllowed(operator string) bool {
	if len(fac.AllowedOperators) == 0 {
		return true // If no operators specified, all operators are allowed
	}

	for _, allowedOperator := range fac.AllowedOperators {
		if allowedOperator == operator {
			return true
		}
	}
	return false
}

// IsFilterCountAllowed checks if the number of filters is within limits
func (fac *FilterAccessConfig) IsFilterCountAllowed(count int) bool {
	return count <= fac.MaxFilters
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

// NewFilterWithAccess creates a new filter with access control validation
func NewFilterWithAccess(field string, operator string, value string, domain any, accessControl AccessControl, ctx context.Context) (*Filter, error) {
	// Validate access control
	if accessControl != nil {
		if err := accessControl.ValidateFilterAccess(ctx, field, operator); err != nil {
			return nil, fmt.Errorf("Filter: %w", err)
		}
	}

	return NewFilter(field, operator, value, domain)
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

// ApplyToQueryBuilder applies the filter to a query builder
func (thiz Filter) ApplyToQueryBuilder(builder dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	switch thiz.Operator {
	case "=":
		return builder.Eq(thiz.Field, thiz.Value)
	case "!=":
		return builder.Neq(thiz.Field, thiz.Value)
	case ">":
		return builder.Gt(thiz.Field, thiz.Value)
	case ">=":
		return builder.Gte(thiz.Field, thiz.Value)
	case "<":
		return builder.Lt(thiz.Field, thiz.Value)
	case "<=":
		return builder.Lte(thiz.Field, thiz.Value)
	case "like":
		return builder.Like(thiz.Field, thiz.Value)
	case "not like":
		return builder.NotLike(thiz.Field, thiz.Value)
	case "in":
		if values, ok := thiz.GetValue().([]string); ok {
			interfaceValues := make([]interface{}, len(values))
			for i, v := range values {
				interfaceValues[i] = v
			}
			return builder.In(thiz.Field, interfaceValues...)
		}
		return builder
	case "not in":
		if values, ok := thiz.GetValue().([]string); ok {
			interfaceValues := make([]interface{}, len(values))
			for i, v := range values {
				interfaceValues[i] = v
			}
			return builder.NotIn(thiz.Field, interfaceValues...)
		}
		return builder
	default:
		return builder
	}
}

type SortParam struct {
	Field string
	Order string
}

func (thiz SortParam) GetField() string {
	return thiz.Field
}

func (thiz SortParam) GetOrder() string {
	return thiz.Order
}

// Query should look like this sort[0][field]=Email&sort[0][order]=desc&sort[1][field]=Id&sort[1][order]=asc
func NewSortParam(field string, order string) (SortParam, error) {
	if field == "" {
		return SortParam{}, fmt.Errorf("SortParam: Field is required")
	}

	if order == "" || (order != "desc" && order != "asc") {
		order = "asc"
	}

	return SortParam{
		Field: field,
		Order: order,
	}, nil
}

func NewPaginationParams(page int, pageSize int, sortParams []SortParam, filters []Filter) *PaginationParams {
	if pageSize == 0 {
		pageSize = 50 //todo
	}

	return &PaginationParams{
		Page:     page,
		PageSize: pageSize,
		Sort:     sortParams,
		Filters:  filters,
	}
}

// NewPaginationParamsWithAccess creates pagination parameters with access control
func NewPaginationParamsWithAccess(page int, pageSize int, sort []SortParam, filters []Filter, accessControl AccessControl, ctx context.Context) (*PaginationParams, error) {
	// Validate access control for all filters
	if accessControl != nil {
		for _, filter := range filters {
			if err := accessControl.ValidateFilterAccess(ctx, filter.Field, filter.Operator); err != nil {
				return nil, fmt.Errorf("PaginationParams: %w", err)
			}
		}

		// Validate access control for all sort parameters
		for _, sortParam := range sort {
			if err := accessControl.ValidateSortAccess(ctx, sortParam.Field); err != nil {
				return nil, fmt.Errorf("PaginationParams: %w", err)
			}
		}
	}

	return NewPaginationParams(page, pageSize, sort, filters), nil
}

type PaginationParams struct {
	Page     int
	PageSize int
	Sort     []SortParam
	Filters  []Filter
}

func (thiz PaginationParams) Offset() int {
	if thiz.Page == 0 {
		thiz.Page = 1
	}
	return (thiz.Page - 1) * thiz.PageSize
}

// ApplyFiltersToQueryBuilder applies all filters to a query builder
func (thiz PaginationParams) ApplyFiltersToQueryBuilder(builder dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	for _, filter := range thiz.Filters {
		builder = filter.ApplyToQueryBuilder(builder)
	}
	return builder
}

// ApplySortToQueryBuilder applies all sort parameters to a query builder
func (thiz PaginationParams) ApplySortToQueryBuilder(builder dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	for _, sort := range thiz.Sort {
		builder = builder.OrderBy(sort.Field, sort.Order)
	}
	return builder
}

// ApplyPaginationToQueryBuilder applies pagination to a query builder
func (thiz PaginationParams) ApplyPaginationToQueryBuilder(builder dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	if thiz.PageSize > 0 {
		builder = builder.Limit(thiz.PageSize)
	}
	if thiz.Page > 0 {
		builder = builder.Offset(thiz.Offset())
	}
	return builder
}

// ApplyAllToQueryBuilder applies all pagination parameters to a query builder
func (thiz PaginationParams) ApplyAllToQueryBuilder(builder dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	builder = thiz.ApplyFiltersToQueryBuilder(builder)
	builder = thiz.ApplySortToQueryBuilder(builder)
	builder = thiz.ApplyPaginationToQueryBuilder(builder)
	return builder
}

// ApplyDBFiltersToQueryBuilder applies database-level RBAC filters to a query builder
func ApplyDBFiltersToQueryBuilder(builder dbCore.IQueryBuilder, dbFilters []DBFilter) dbCore.IQueryBuilder {
	if len(dbFilters) == 0 {
		return builder
	}

	// Group filters by logic (AND/OR)
	var andFilters []DBFilter
	var orFilters []DBFilter

	for _, filter := range dbFilters {
		if filter.Logic == "OR" {
			orFilters = append(orFilters, filter)
		} else {
			andFilters = append(andFilters, filter)
		}
	}

	// Apply AND filters first
	for _, filter := range andFilters {
		builder = applyDBFilterToQueryBuilder(builder, filter)
	}

	// Apply OR filters as a group
	if len(orFilters) > 0 {
		var orConditions []dbCore.Condition
		for _, filter := range orFilters {
			// Create a temporary builder to build the condition
			tempBuilder := v2.NewBuilder()
			tempBuilder = applyDBFilterToQueryBuilder(tempBuilder, filter).(*v2.Builder)
			// Extract the condition from the builder
			query := tempBuilder.Build()
			if query.Where != nil && len(query.Where.Conditions) > 0 {
				// Add all conditions from the temporary builder
				orConditions = append(orConditions, query.Where.Conditions...)
			}
		}
		if len(orConditions) > 0 {
			builder = builder.Where(&v2.CompositeCondition{
				Operator:   "OR",
				Conditions: orConditions,
			})
		}
	}

	return builder
}

// applyDBFilterToQueryBuilder applies a single DB filter to a query builder
func applyDBFilterToQueryBuilder(builder dbCore.IQueryBuilder, filter DBFilter) dbCore.IQueryBuilder {
	// Handle subqueries
	if filter.Subquery != nil {
		return applySubqueryToQueryBuilder(builder, filter)
	}

	// Handle joins
	if filter.Join != nil {
		builder = applyJoinToQueryBuilder(builder, *filter.Join)
	}

	// Apply the filter condition
	switch filter.Operator {
	case "=":
		return builder.Eq(filter.Field, filter.Value)
	case "!=":
		return builder.Neq(filter.Field, filter.Value)
	case ">":
		return builder.Gt(filter.Field, filter.Value)
	case ">=":
		return builder.Gte(filter.Field, filter.Value)
	case "<":
		return builder.Lt(filter.Field, filter.Value)
	case "<=":
		return builder.Lte(filter.Field, filter.Value)
	case "like":
		return builder.Like(filter.Field, filter.Value)
	case "not like":
		return builder.NotLike(filter.Field, filter.Value)
	case "in":
		if values, ok := filter.Value.([]interface{}); ok {
			return builder.In(filter.Field, values...)
		}
		return builder
	case "not in":
		if values, ok := filter.Value.([]interface{}); ok {
			return builder.NotIn(filter.Field, values...)
		}
		return builder
	default:
		return builder
	}
}

// applySubqueryToQueryBuilder applies a subquery filter to a query builder
func applySubqueryToQueryBuilder(builder dbCore.IQueryBuilder, filter DBFilter) dbCore.IQueryBuilder {
	subquery := filter.Subquery

	// Build subquery
	subqueryBuilder := v2.NewBuilder()
	subqueryBuilder = subqueryBuilder.Select(subquery.Select).From(subquery.Table).(*v2.Builder)

	// Apply joins to subquery
	for _, join := range subquery.Join {
		subqueryBuilder = applyJoinToQueryBuilder(subqueryBuilder, join).(*v2.Builder)
	}

	// Apply where conditions to subquery
	for _, whereFilter := range subquery.Where {
		subqueryBuilder = applyDBFilterToQueryBuilder(subqueryBuilder, whereFilter).(*v2.Builder)
	}

	// Build the subquery to get the Query object
	subqueryQuery := subqueryBuilder.Build()

	// Apply subquery to main query
	switch subquery.Operator {
	case "IN":
		return builder.InSubquery(filter.Field, subqueryQuery)
	case "NOT IN":
		return builder.NotInSubquery(filter.Field, subqueryQuery)
	case "EXISTS":
		sql, args, err := subqueryBuilder.ToSQL()
		if err != nil {
			log.Log().Errorf("pagination: cannot render EXISTS subquery: %v", err)
			return builder
		}
		return builder.Where(&dbCore.RawCondition{
			SQL:  fmt.Sprintf("EXISTS (%s)", sql),
			Args: args,
		})
	case "NOT EXISTS":
		sql, args, err := subqueryBuilder.ToSQL()
		if err != nil {
			log.Log().Errorf("pagination: cannot render NOT EXISTS subquery: %v", err)
			return builder
		}
		return builder.Where(&dbCore.RawCondition{
			SQL:  fmt.Sprintf("NOT EXISTS (%s)", sql),
			Args: args,
		})
	default:
		return builder
	}
}

// applyJoinToQueryBuilder applies a join to a query builder
func applyJoinToQueryBuilder(builder dbCore.IQueryBuilder, join DBJoin) dbCore.IQueryBuilder {
	switch join.Type {
	case "INNER":
		return builder.InnerJoin(join.Table, &dbCore.BinaryCondition{
			Left:     join.LeftKey,
			Operator: "=",
			Right:    join.RightKey,
		})
	case "LEFT":
		return builder.LeftJoin(join.Table, &dbCore.BinaryCondition{
			Left:     join.LeftKey,
			Operator: "=",
			Right:    join.RightKey,
		})
	case "RIGHT":
		return builder.RightJoin(join.Table, &dbCore.BinaryCondition{
			Left:     join.LeftKey,
			Operator: "=",
			Right:    join.RightKey,
		})
	case "FULL":
		return builder.FullJoin(join.Table, &dbCore.BinaryCondition{
			Left:     join.LeftKey,
			Operator: "=",
			Right:    join.RightKey,
		})
	default:
		return builder
	}
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
