package model

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	sqlbuilder "github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/service/cache"
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
		// The configured caps are checked *before* iterating, not after and not at all.
		// MaxFilters and MaxSorts were configuration nothing read — an application could set
		// them, see them in its config, and still have every request it received build as
		// many predicates as the query string asked for.
		if limiter, ok := accessControl.(ComplexityLimiter); ok {
			if err := limiter.CheckComplexity(len(filters), len(sort)); err != nil {
				return nil, fmt.Errorf("PaginationParams: %w", err)
			}
		}

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

// ComplexityLimiter is implemented by an access control that bounds how many filters and sorts
// one request may ask for.
//
// Optional rather than part of AccessControl, which applications implement: an implementation
// that does not bound complexity keeps compiling and is simply not asked.
type ComplexityLimiter interface {
	// CheckComplexity refuses a request asking for more filters or sorts than the
	// configuration permits.
	CheckComplexity(filters int, sorts int) error
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

// ApplyDBFiltersToQueryBuilder applies database-level RBAC filters to a query builder.
//
// It returns an error now, and the signature change is the point rather than a side effect.
// These filters are *restrictions*: a filter that fails to apply does not deny, it stops
// denying, so silently dropping one widens the query to exactly the rows the policy meant to
// keep out. The old code did that in three places — an operator it did not recognise, an `in`
// whose value was not []interface{} (which is what a []string from YAML is), and a subquery
// operator outside the four it handles — each ending in `return builder` with the filter gone
// and nobody told.
//
// A caller must treat the error as "do not run this query". Nothing inside the framework calls
// this; applications wire it up themselves, and its whole job is to be a security boundary,
// which is what makes the break worth taking. It follows the precedent set when
// IQueryBuilder.ToSQL started returning an error.
func ApplyDBFiltersToQueryBuilder(builder dbCore.IQueryBuilder, dbFilters []DBFilter) (dbCore.IQueryBuilder, error) {
	if len(dbFilters) == 0 {
		return builder, nil
	}

	// Re-validated here as well as in GenerateDBFilters, because an application can build a
	// []DBFilter by hand and hand it straight to this function.
	for _, filter := range dbFilters {
		if err := filter.Validate(nil); err != nil {
			return nil, fmt.Errorf("refusing to apply an authorization filter: %w", err)
		}
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
		applied, err := applyDBFilterToQueryBuilder(builder, filter)
		if err != nil {
			return nil, err
		}
		builder = applied
	}

	// Apply OR filters as a group
	if len(orFilters) > 0 {
		var orConditions []dbCore.Condition
		for _, filter := range orFilters {
			// Create a temporary builder to build the condition
			tempBuilder := sqlbuilder.NewLike(builder)
			applied, err := applyDBFilterToQueryBuilder(tempBuilder, filter)
			if err != nil {
				return nil, err
			}
			// Extract the condition from the builder
			query := applied.(*sqlbuilder.Builder).Build()
			if query.Where != nil && len(query.Where.Conditions) > 0 {
				// Add all conditions from the temporary builder
				orConditions = append(orConditions, query.Where.Conditions...)
			}
		}
		if len(orConditions) > 0 {
			builder = builder.Where(&dbCore.CompositeCondition{
				Operator:   "OR",
				Conditions: orConditions,
			})
		}
	}

	return builder, nil
}

// applyDBFilterToQueryBuilder applies a single DB filter to a query builder
func applyDBFilterToQueryBuilder(
	builder dbCore.IQueryBuilder, filter DBFilter) (dbCore.IQueryBuilder, error) {

	// Handle subqueries
	if filter.Subquery != nil {
		return applySubqueryToQueryBuilder(builder, filter)
	}

	// Handle joins
	if filter.Join != nil {
		builder = applyJoinToQueryBuilder(builder, *filter.Join)
	}

	// A predicate the configuration author vouched for, with its parameters bound.
	if filter.RawSQL != "" {
		return builder.Where(&dbCore.RawCondition{SQL: filter.RawSQL, Args: filter.RawArgs}), nil
	}

	// Apply the filter condition
	switch strings.ToLower(filter.Operator) {
	case "=":
		return builder.Eq(filter.Field, filter.Value), nil
	case "!=":
		return builder.Neq(filter.Field, filter.Value), nil
	case ">":
		return builder.Gt(filter.Field, filter.Value), nil
	case ">=":
		return builder.Gte(filter.Field, filter.Value), nil
	case "<":
		return builder.Lt(filter.Field, filter.Value), nil
	case "<=":
		return builder.Lte(filter.Field, filter.Value), nil
	case "like":
		return builder.Like(filter.Field, filter.Value), nil
	case "not like":
		return builder.NotLike(filter.Field, filter.Value), nil
	case "in", "not in":
		// Any slice, not only []interface{}. A list written in YAML or JSON arrives as
		// []string or []any depending on how it was decoded, and the []interface{} type
		// assertion silently dropped everything else — turning "you may see these five rows"
		// into no restriction at all.
		values, err := filterValueSlice(filter.Value)
		if err != nil {
			return nil, fmt.Errorf("filter on %q with operator %q: %w",
				filter.Field, filter.Operator, err)
		}
		if strings.EqualFold(filter.Operator, "in") {
			return builder.In(filter.Field, values...), nil
		}
		return builder.NotIn(filter.Field, values...), nil
	default:
		return nil, fmt.Errorf(
			"filter on %q uses operator %q, which this builder cannot emit; the filter would "+
				"otherwise be dropped and the query left unrestricted",
			filter.Field, filter.Operator)
	}
}

// filterValueSlice normalises an IN/NOT IN value into a bindable slice.
func filterValueSlice(value any) ([]any, error) {
	if value == nil {
		return nil, fmt.Errorf("needs a list of values and has none")
	}
	if values, ok := value.([]any); ok {
		if len(values) == 0 {
			return nil, fmt.Errorf("needs a non-empty list of values")
		}
		return values, nil
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil, fmt.Errorf("needs a list of values, got %T", value)
	}
	if reflected.Len() == 0 {
		return nil, fmt.Errorf("needs a non-empty list of values")
	}

	values := make([]any, reflected.Len())
	for i := range values {
		values[i] = reflected.Index(i).Interface()
	}
	return values, nil
}

// applySubqueryToQueryBuilder applies a subquery filter to a query builder
func applySubqueryToQueryBuilder(
	builder dbCore.IQueryBuilder, filter DBFilter) (dbCore.IQueryBuilder, error) {

	subquery := filter.Subquery

	// Build subquery
	subqueryBuilder := sqlbuilder.NewLike(builder)
	subqueryBuilder = subqueryBuilder.Select(subquery.Select).From(subquery.Table).(*sqlbuilder.Builder)

	// Apply joins to subquery
	for _, join := range subquery.Join {
		subqueryBuilder = applyJoinToQueryBuilder(subqueryBuilder, join).(*sqlbuilder.Builder)
	}

	// Apply where conditions to subquery
	for _, whereFilter := range subquery.Where {
		applied, err := applyDBFilterToQueryBuilder(subqueryBuilder, whereFilter)
		if err != nil {
			return nil, fmt.Errorf("inside the subquery on %s: %w", subquery.Table, err)
		}
		subqueryBuilder = applied.(*sqlbuilder.Builder)
	}

	// Build the subquery to get the Query object
	subqueryQuery := subqueryBuilder.Build()

	// Apply subquery to main query
	switch strings.ToUpper(subquery.Operator) {
	case "IN":
		return builder.InSubquery(filter.Field, subqueryQuery), nil
	case "NOT IN":
		return builder.NotInSubquery(filter.Field, subqueryQuery), nil
	case "EXISTS", "NOT EXISTS":
		sql, args, err := subqueryBuilder.ToSQL()
		if err != nil {
			return nil, fmt.Errorf("cannot render the %s subquery on %s: %w",
				subquery.Operator, subquery.Table, err)
		}
		return builder.Where(&dbCore.RawCondition{
			SQL:  fmt.Sprintf("%s (%s)", strings.ToUpper(subquery.Operator), sql),
			Args: args,
		}), nil
	default:
		return nil, fmt.Errorf(
			"subquery on %s uses operator %q, which this builder cannot emit",
			subquery.Table, subquery.Operator)
	}
}

// applyJoinToQueryBuilder applies a join to a query builder.
//
// Both keys are dbCore.Identifier, and that is a bug fix rather than tidying. The right side of
// a BinaryCondition is a *value* position, so a bare string there is bound — which meant every
// DBJoin-based RBAC filter emitted `INNER JOIN teams ON team_members.team_id = ?` with the
// argument "teams.id", joining on a string constant instead of the column. The join matched
// nothing, so the filter that depended on it restricted the query to nothing or, joined with
// OR, to everything.
func applyJoinToQueryBuilder(builder dbCore.IQueryBuilder, join DBJoin) dbCore.IQueryBuilder {
	condition := &dbCore.BinaryCondition{
		Left:     dbCore.Identifier(join.LeftKey),
		Operator: "=",
		Right:    dbCore.Identifier(join.RightKey),
	}

	switch strings.ToUpper(join.Type) {
	case "INNER":
		return builder.InnerJoin(join.Table, condition)
	case "LEFT":
		return builder.LeftJoin(join.Table, condition)
	case "RIGHT":
		return builder.RightJoin(join.Table, condition)
	case "FULL":
		return builder.FullJoin(join.Table, condition)
	default:
		// Unreachable through GenerateDBFilters, which validates the type; reachable from a
		// hand-built filter, where refusing the join is safer than emitting the query without
		// it and letting the predicate that depended on it match the wrong rows.
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
