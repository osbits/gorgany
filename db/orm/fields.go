package orm

import (
	"reflect"
	"strings"
	"sync"

	"github.com/osbits/gorgany/v2/util"
	"gorm.io/gorm/schema"
)

// extractFieldsForInsert extracts fields from a struct and its embedded structs for INSERT operations
func extractFieldsForInsert(val reflect.Value, meta *EntityMeta) ([]string, []interface{}) {
	var columns []string
	var values []interface{}
	columnMap := make(map[string]bool)
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(val.Interface(), schemaCache, schema.NamingStrategy{})
	relations := map[string]struct{}{}
	if err == nil && entitySchema != nil {
		for relName := range entitySchema.Relationships.Relations {
			relations[relName] = struct{}{}
		}
	}
	extractFieldsFromStructSkipRelations(val, meta, &columns, &values, columnMap, false, entitySchema, err == nil, relations)
	return columns, values
}

// extractFieldsFromStructSkipRelations is like extractFieldsFromStruct but skips relation fields
func extractFieldsFromStructSkipRelations(val reflect.Value, meta *EntityMeta, columns *[]string, values *[]interface{}, columnMap map[string]bool, isEmbedded bool, entitySchema *schema.Schema, hasSchema bool, relations map[string]struct{}) {
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := val.Type().Field(i)
		if !field.CanInterface() {
			continue
		}
		if fieldType.Anonymous && util.IndirectType(fieldType.Type).Kind() == reflect.Struct {
			if fieldType.Name == "BaseEntity" || fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
				continue
			}
			extractFieldsFromStructSkipRelations(field, meta, columns, values, columnMap, true, entitySchema, hasSchema, relations)
			continue
		}
		if fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
			continue
		}
		// Skip relation fields
		if _, isRelation := relations[fieldType.Name]; isRelation {
			continue
		}
		var columnName string
		if hasSchema && entitySchema != nil {
			if field, ok := entitySchema.FieldsByName[fieldType.Name]; ok {
				columnName = field.DBName
			} else {
				columnName = fieldType.Name
			}
		} else {
			columnName = fieldType.Name
			gormTag := fieldType.Tag.Get("gorm")
			if gormTag != "" {
				parts := strings.Split(gormTag, ";")
				for _, part := range parts {
					if strings.HasPrefix(part, "column:") {
						columnName = strings.TrimPrefix(part, "column:")
						break
					}
				}
			}
		}
		if columnMap[columnName] {
			continue
		}
		if columnName == meta.PrimaryKey && isPKAutoIncrement(val.Interface(), fieldType.Name) {
			if isZeroValue(field.Interface()) {
				continue
			}
		}
		*columns = append(*columns, columnName)
		*values = append(*values, field.Interface())
		columnMap[columnName] = true
	}
}

// extractFieldsForUpdate returns the SET list for an UPDATE of val: every column it maps,
// narrowed by meta.DirtyColumns, and never a primary key column.
//
// Only the first key column used to be left out, because it was the only one the WHERE
// named. On a composite key the other parts were written back into the row as well — a
// no-op at best, and on a key the server generates or a table whose key another system owns,
// a write nobody asked for. Every key column is now left out, since pkPredicate puts every
// one of them in the WHERE.
func extractFieldsForUpdate(val reflect.Value, meta *EntityMeta) map[string]interface{} {
	updateFields := make(map[string]interface{})
	columnMap := make(map[string]bool)
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(val.Interface(), schemaCache, schema.NamingStrategy{})
	relations := map[string]struct{}{}
	keyColumns := map[string]bool{meta.PrimaryKey: true}
	if err == nil && entitySchema != nil {
		for relName := range entitySchema.Relationships.Relations {
			relations[relName] = struct{}{}
		}
		if len(entitySchema.PrimaryFieldDBNames) > 0 {
			keyColumns = make(map[string]bool, len(entitySchema.PrimaryFieldDBNames))
			for _, column := range entitySchema.PrimaryFieldDBNames {
				keyColumns[column] = true
			}
		}
	}
	extractUpdateFieldsFromStruct(val, meta, keyColumns, updateFields, columnMap, relations, entitySchema, err == nil)
	return updateFields
}

func extractUpdateFieldsFromStruct(val reflect.Value, meta *EntityMeta, keyColumns map[string]bool, updateFields map[string]interface{}, columnMap map[string]bool, relations map[string]struct{}, entitySchema *schema.Schema, hasSchema bool) {
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := val.Type().Field(i)
		if !field.CanInterface() {
			continue
		}
		if fieldType.Anonymous && util.IndirectType(fieldType.Type).Kind() == reflect.Struct {
			if fieldType.Name == "BaseEntity" || fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
				continue
			}
			extractUpdateFieldsFromStruct(field, meta, keyColumns, updateFields, columnMap, relations, entitySchema, hasSchema)
			continue
		}
		if fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
			continue
		}
		// Skip relation fields
		if _, isRelation := relations[fieldType.Name]; isRelation {
			continue
		}
		var columnName string
		if hasSchema && entitySchema != nil {
			if field, ok := entitySchema.FieldsByName[fieldType.Name]; ok {
				columnName = field.DBName
			} else {
				columnName = fieldType.Name
			}
		} else {
			columnName = fieldType.Name
			gormTag := fieldType.Tag.Get("gorm")
			if gormTag != "" {
				parts := strings.Split(gormTag, ";")
				for _, part := range parts {
					if strings.HasPrefix(part, "column:") {
						columnName = strings.TrimPrefix(part, "column:")
						break
					}
				}
			}
		}
		if columnMap[columnName] {
			continue
		}
		// A key column identifies the row rather than being written to it, so it is never in
		// the SET list: the WHERE carries it instead. A DirtyColumns set that names one is
		// refused before this is reached (see refuseKeyChange), since leaving the changed key
		// out of the SET while the WHERE reads it would write to whichever row holds the new
		// key.
		if !keyColumns[columnName] && meta.isDirtyColumn(columnName) {
			updateFields[columnName] = field.Interface()
		}
		columnMap[columnName] = true
	}
}

func isPKAutoIncrement(entity interface{}, fieldName string) bool {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return false
	}
	field, found := val.Type().FieldByName(fieldName)
	if found {
		return isFieldAutoIncrement(field, val.FieldByName(fieldName))
	}
	return findAutoIncrementFieldInEmbedded(val, fieldName)
}

func isFieldAutoIncrement(field reflect.StructField, fieldValue reflect.Value) bool {
	gormTag := field.Tag.Get("gorm")
	if gormTag == "" {
		return false
	}
	parts := strings.Split(gormTag, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "autoIncrement" || part == "auto_increment" {
			return true
		}
	}
	isPrimary := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "primaryKey" || part == "primary_key" {
			isPrimary = true
			break
		}
	}
	if isPrimary && fieldValue.IsValid() {
		switch fieldValue.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return true
		}
	}
	return false
}

func findAutoIncrementFieldInEmbedded(val reflect.Value, fieldName string) bool {
	valType := val.Type()
	for i := 0; i < valType.NumField(); i++ {
		field := valType.Field(i)
		if !field.Anonymous {
			continue
		}
		if !val.Field(i).CanInterface() {
			continue
		}
		if field.Name == "BaseEntity" || field.Name == "Meta" || field.Tag.Get("gorm") == "-" {
			continue
		}
		embeddedVal := val.Field(i)
		if embeddedVal.Kind() == reflect.Ptr {
			if embeddedVal.IsNil() {
				continue
			}
			embeddedVal = embeddedVal.Elem()
		}
		if embeddedVal.Kind() != reflect.Struct {
			continue
		}
		field, found := embeddedVal.Type().FieldByName(fieldName)
		if found {
			return isFieldAutoIncrement(field, embeddedVal.FieldByName(fieldName))
		}
		if findAutoIncrementFieldInEmbedded(embeddedVal, fieldName) {
			return true
		}
	}
	return false
}

func isZeroValue(v interface{}) bool {
	return reflect.ValueOf(v).IsZero()
}

func isNilValue(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
