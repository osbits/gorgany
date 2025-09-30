package orm

import (
	"reflect"
	"strings"
	"sync"

	"github.com/gorganyio/gorgany/util"
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

func extractFieldsForUpdate(val reflect.Value, meta *EntityMeta) (interface{}, map[string]interface{}) {
	var pkValue interface{}
	updateFields := make(map[string]interface{})
	columnMap := make(map[string]bool)
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(val.Interface(), schemaCache, schema.NamingStrategy{})
	relations := map[string]struct{}{}
	if err == nil && entitySchema != nil {
		for relName := range entitySchema.Relationships.Relations {
			relations[relName] = struct{}{}
		}
	}
	extractUpdateFieldsFromStruct(val, meta, &pkValue, updateFields, columnMap, relations, entitySchema, err == nil)
	return pkValue, updateFields
}

func extractUpdateFieldsFromStruct(val reflect.Value, meta *EntityMeta, pkValue *interface{}, updateFields map[string]interface{}, columnMap map[string]bool, relations map[string]struct{}, entitySchema *schema.Schema, hasSchema bool) {
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
			extractUpdateFieldsFromStruct(field, meta, pkValue, updateFields, columnMap, relations, entitySchema, hasSchema)
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
		if columnName == meta.PrimaryKey {
			*pkValue = field.Interface()
		} else {
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

func findFieldValueInEmbedded(val reflect.Value, fieldName string) interface{} {
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
		fieldVal := embeddedVal.FieldByName(fieldName)
		if fieldVal.IsValid() {
			return fieldVal.Interface()
		}
		if result := findFieldValueInEmbedded(embeddedVal, fieldName); result != nil {
			return result
		}
	}
	return nil
}
