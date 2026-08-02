package model

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/osbits/gorgany/v2/app/core"
)

// FieldFilteredDto wraps a DTO and provides field filtering based on access control
type FieldFilteredDto struct {
	dto           interface{}
	accessControl AccessControl
	ctx           context.Context
	entity        core.Authenticable
}

// NewFieldFilteredDto creates a new field-filtered DTO wrapper
func NewFieldFilteredDto(dto interface{}, accessControl AccessControl, ctx context.Context, entity core.Authenticable) *FieldFilteredDto {
	return &FieldFilteredDto{
		dto:           dto,
		accessControl: accessControl,
		ctx:           ctx,
		entity:        entity,
	}
}

// MarshalJSON implements json.Marshaler interface with field filtering.
//
// This runs once per DTO, so a collection or a page of results runs it once per row, and
// each run asks the access control which fields the caller may read. That question resolves
// the caller's identity, which for a session-backed strategy means a session lookup and a
// user load - so the identity memo on the request context is what keeps a hundred-row
// response to one of each instead of a hundred. See RoleBasedAccessControl.resolveUserContext:
// the fields are not cached here on purpose, because the DTO wrapper is per row and would
// have nowhere to cache them that the next row could see.
func (ffd *FieldFilteredDto) MarshalJSON() ([]byte, error) {
	// Get readable fields for the current user
	readableFields := ffd.accessControl.GetReadableFields(ffd.ctx, ffd.entity)

	// Create a new DTO instance to avoid modifying the original
	dtoValue := reflect.ValueOf(ffd.dto)
	if dtoValue.Kind() == reflect.Ptr {
		dtoValue = dtoValue.Elem()
	}

	// Create a new instance of the same type
	newDto := reflect.New(dtoValue.Type()).Interface()

	// Copy only the readable fields
	ffd.copyReadableFields(ffd.dto, newDto, readableFields)

	// Use the existing DTO marshaling logic with field filtering
	if marshaller, ok := newDto.(core.LimitedFieldsMarshaller); ok {
		// Create a field filtering marshaller
		fieldFilteringMarshaller := NewFieldFilteringMarshaller(marshaller, ffd.accessControl, ffd.ctx, ffd.entity)

		// Use the field filtering marshaller for JSON marshaling
		return json.Marshal(fieldFilteringMarshaller)
	}

	// Fallback to standard JSON marshaling
	return json.Marshal(newDto)
}

// copyReadableFields copies only the readable fields from source to destination
func (ffd *FieldFilteredDto) copyReadableFields(source, dest interface{}, readableFields []string) {
	sourceValue := reflect.ValueOf(source)
	destValue := reflect.ValueOf(dest)

	if sourceValue.Kind() == reflect.Ptr {
		sourceValue = sourceValue.Elem()
	}
	if destValue.Kind() == reflect.Ptr {
		destValue = destValue.Elem()
	}

	sourceType := sourceValue.Type()
	destType := destValue.Type()

	// Create a map of readable field names for quick lookup
	readableMap := make(map[string]bool)
	for _, field := range readableFields {
		readableMap[field] = true
	}

	// Copy fields that are readable
	for i := 0; i < sourceType.NumField(); i++ {
		sourceField := sourceType.Field(i)
		fieldName := sourceField.Name

		// Check if this field is readable
		if !readableMap[fieldName] {
			continue
		}

		// Find corresponding field in destination
		if destField, exists := destType.FieldByName(fieldName); exists {
			if destField.Type == sourceField.Type {
				sourceFieldValue := sourceValue.Field(i)
				destFieldValue := destValue.FieldByName(fieldName)

				if destFieldValue.CanSet() {
					destFieldValue.Set(sourceFieldValue)
				}
			}
		}
	}
}

// FieldFilteredCollection wraps a collection of DTOs with field filtering
type FieldFilteredCollection struct {
	collection    []interface{}
	accessControl AccessControl
	ctx           context.Context
	entity        core.Authenticable
}

// NewFieldFilteredCollection creates a new field-filtered collection wrapper
func NewFieldFilteredCollection(collection []interface{}, accessControl AccessControl, ctx context.Context, entity core.Authenticable) *FieldFilteredCollection {
	return &FieldFilteredCollection{
		collection:    collection,
		accessControl: accessControl,
		ctx:           ctx,
		entity:        entity,
	}
}

// MarshalJSON implements json.Marshaler interface for collections
func (ffc *FieldFilteredCollection) MarshalJSON() ([]byte, error) {
	var filteredCollection []interface{}

	for _, item := range ffc.collection {
		// Create a field-filtered wrapper for each item
		filteredItem := NewFieldFilteredDto(item, ffc.accessControl, ffc.ctx, ffc.entity)
		filteredCollection = append(filteredCollection, filteredItem)
	}

	// Marshal the filtered collection
	return json.Marshal(filteredCollection)
}

// FieldFilteredPagination wraps paginated results with field filtering
type FieldFilteredPagination struct {
	items         []interface{}
	total         int64
	page          int
	perPage       int
	accessControl AccessControl
	ctx           context.Context
	entity        core.Authenticable
}

// NewFieldFilteredPagination creates a new field-filtered pagination wrapper
func NewFieldFilteredPagination(items []interface{}, total int64, page int, perPage int, accessControl AccessControl, ctx context.Context, entity core.Authenticable) *FieldFilteredPagination {
	return &FieldFilteredPagination{
		items:         items,
		total:         total,
		page:          page,
		perPage:       perPage,
		accessControl: accessControl,
		ctx:           ctx,
		entity:        entity,
	}
}

// MarshalJSON implements json.Marshaler interface for pagination
func (ffp *FieldFilteredPagination) MarshalJSON() ([]byte, error) {
	// Create field-filtered items
	var filteredItems []interface{}
	for _, item := range ffp.items {
		filteredItem := NewFieldFilteredDto(item, ffp.accessControl, ffp.ctx, ffp.entity)
		filteredItems = append(filteredItems, filteredItem)
	}

	// Create pagination structure
	pagination := map[string]interface{}{
		"items":   filteredItems,
		"total":   ffp.total,
		"page":    ffp.page,
		"perPage": ffp.perPage,
		"pages":   (ffp.total + int64(ffp.perPage) - 1) / int64(ffp.perPage),
	}

	return json.Marshal(pagination)
}

// FieldFilteringMarshaller extends LimitedFieldsMarshaller with access control
type FieldFilteringMarshaller struct {
	LimitedFieldsMarshaller core.LimitedFieldsMarshaller
	AccessControl           AccessControl
	Ctx                     context.Context
	Entity                  core.Authenticable
}

// NewFieldFilteringMarshaller creates a new field filtering marshaller
func NewFieldFilteringMarshaller(marshaller core.LimitedFieldsMarshaller, accessControl AccessControl, ctx context.Context, entity core.Authenticable) *FieldFilteringMarshaller {
	return &FieldFilteringMarshaller{
		LimitedFieldsMarshaller: marshaller,
		AccessControl:           accessControl,
		Ctx:                     ctx,
		Entity:                  entity,
	}
}

// AllowedFields returns the fields allowed for the current user
func (ffm *FieldFilteringMarshaller) AllowedFields() []string {
	if ffm.AccessControl == nil {
		return ffm.LimitedFieldsMarshaller.AllowedFields()
	}

	return ffm.AccessControl.GetReadableFields(ffm.Ctx, ffm.Entity)
}

// AllowedProtectedFields returns the protected fields allowed for the current user
func (ffm *FieldFilteringMarshaller) AllowedProtectedFields() []string {
	// For now, return the same as allowed fields
	// This can be enhanced to handle protected fields separately
	return ffm.AllowedFields()
}

// SetAllowedFields sets the allowed fields (for compatibility)
func (ffm *FieldFilteringMarshaller) SetAllowedFields(fields []string) {
	// This method is called by the marshaling system
	// We override it to use access control instead
	// The actual fields are determined by GetReadableFields
}

// FieldFilteringBuilder provides a fluent interface for building field-filtered DTOs
type FieldFilteringBuilder struct {
	accessControl AccessControl
	ctx           context.Context
	entity        core.Authenticable
}

// NewFieldFilteringBuilder creates a new field filtering builder
func NewFieldFilteringBuilder(accessControl AccessControl, ctx context.Context, entity core.Authenticable) *FieldFilteringBuilder {
	return &FieldFilteringBuilder{
		accessControl: accessControl,
		ctx:           ctx,
		entity:        entity,
	}
}

// FilterDto wraps a single DTO with field filtering
func (b *FieldFilteringBuilder) FilterDto(dto interface{}) *FieldFilteredDto {
	return NewFieldFilteredDto(dto, b.accessControl, b.ctx, b.entity)
}

// FilterCollection wraps a collection with field filtering
func (b *FieldFilteringBuilder) FilterCollection(collection []interface{}) *FieldFilteredCollection {
	return NewFieldFilteredCollection(collection, b.accessControl, b.ctx, b.entity)
}

// FilterPagination wraps paginated results with field filtering
func (b *FieldFilteringBuilder) FilterPagination(items []interface{}, total int64, page int, perPage int) *FieldFilteredPagination {
	return NewFieldFilteredPagination(items, total, page, perPage, b.accessControl, b.ctx, b.entity)
}

// CreateMarshaller creates a field filtering marshaller
func (b *FieldFilteringBuilder) CreateMarshaller(marshaller core.LimitedFieldsMarshaller) *FieldFilteringMarshaller {
	return NewFieldFilteringMarshaller(marshaller, b.accessControl, b.ctx, b.entity)
}
