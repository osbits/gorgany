package orm

import (
	"reflect"
	"strings"
	"sync"

	"github.com/osbits/gorgany/v2/util"
	"gorm.io/gorm/schema"
)

// extractFieldsForInsert returns the column list and values of an INSERT of val: every column
// it maps, including those of its embedded structs, that gorm's permissions let a create write.
//
// With a schema, a field gorm will not create is left out: gorm:"->" (read-only, such as a
// rowversion or a computed column the server refuses to be written), "<-:false",
// "<-:update" and "-:all". They used to be written all the same, because the walk read only
// the column name from the schema, so a model could not map a column it must never write. The
// permissions are gorm's own, and an ordinary field — no permission tag — is creatable, so
// its INSERT is what it was.
//
// A zero primary key is left to the server when the schema calls it auto-increment: an
// autoIncrement tag, or the single integer key gorm makes one by default, such as an ID int
// with no primaryKey or autoIncrement tag, whatever other tag it has. It used to be judged by
// a tag heuristic that took any integer primaryKey for auto-increment, so a key tagged
// autoIncrement:false, and the first part of a composite key, were left out whenever they were
// zero, and an ID without a primaryKey tag was written as 0. Without a schema
// that heuristic (isPKAutoIncrement) is still the rule, and only the tag segment "->" is
// honoured, since the other permissions are gorm's to parse.
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
		schemaField := schemaFieldFor(entitySchema, hasSchema, fieldType.Name)
		if !fieldIsWritten(schemaField, fieldType, true) {
			continue
		}
		columnName := fieldColumnName(schemaField, fieldType)
		if columnMap[columnName] {
			continue
		}
		if schemaField != nil {
			if schemaField.PrimaryKey && schemaField.AutoIncrement && isZeroValue(field.Interface()) {
				continue
			}
		} else if columnName == meta.PrimaryKey && isPKAutoIncrement(val.Interface(), fieldType.Name) {
			if isZeroValue(field.Interface()) {
				continue
			}
		}
		*columns = append(*columns, columnName)
		*values = append(*values, field.Interface())
		columnMap[columnName] = true
	}
}

// extractFieldsForUpdate returns the SET list for an UPDATE of val: every column it maps that
// gorm's permissions let an update write, narrowed by meta.DirtyColumns, and never a primary key
// column.
//
// A field gorm will not update is left out, as extractFieldsForInsert leaves out one it will
// not create: gorm:"->", "<-:false", "<-:create" and "-:all". A create-only column, such as
// who created a row, is written once and never again, and a read-only one never. Without a
// schema only the tag segment "->" is honoured.
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
		schemaField := schemaFieldFor(entitySchema, hasSchema, fieldType.Name)
		if !fieldIsWritten(schemaField, fieldType, false) {
			continue
		}
		columnName := fieldColumnName(schemaField, fieldType)
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

// schemaFieldFor returns the schema's field for the Go field name, or nil when there is no
// schema or the schema does not know the field.
func schemaFieldFor(entitySchema *schema.Schema, hasSchema bool, name string) *schema.Field {
	if !hasSchema || entitySchema == nil {
		return nil
	}
	return entitySchema.FieldsByName[name]
}

// fieldIsWritten reports whether an INSERT (create) or an UPDATE writes the field, by gorm's
// permissions when the schema knows it and by the tag segment "->" when it does not.
//
// A schema field with no column is never written either. gorm leaves the column empty for a
// field it ignores, such as gorm:"-:all", and the walk used to write that field under an empty
// column name.
func fieldIsWritten(schemaField *schema.Field, fieldType reflect.StructField, create bool) bool {
	if schemaField != nil {
		if schemaField.DBName == "" {
			return false
		}
		if create {
			return schemaField.Creatable
		}
		return schemaField.Updatable
	}
	return !hasReadOnlyTagSegment(fieldType.Tag.Get("gorm"))
}

// hasReadOnlyTagSegment reports whether a gorm tag holds the read-only permission "->", alone
// or as "->:false", which gorm reads as neither written nor read.
func hasReadOnlyTagSegment(gormTag string) bool {
	for _, part := range strings.Split(gormTag, ";") {
		part = strings.TrimSpace(part)
		if part == "->" || strings.HasPrefix(part, "->:") {
			return true
		}
	}
	return false
}

// fieldColumnName returns the column a field is written to: the schema's column when the schema
// knows the field, and otherwise the tag's column: segment, or the Go field name.
func fieldColumnName(schemaField *schema.Field, fieldType reflect.StructField) string {
	if schemaField != nil {
		return schemaField.DBName
	}
	for _, part := range strings.Split(fieldType.Tag.Get("gorm"), ";") {
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return fieldType.Name
}

// isPKAutoIncrement is the zero-key rule without a schema: whether a key field is one the
// server generates, judged from its tag alone. With a schema, gorm's AutoIncrement decides
// instead (see extractFieldsForInsert).
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
