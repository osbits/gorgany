package orm

import (
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"reflect"
	"strings"

	"gorm.io/gorm/schema"
)

func GetValueInTag(tag reflect.StructTag, param string) string {
	gormTag := tag.Get(core.GorganyORMTag)
	splitGormTag := strings.Split(gormTag, ",")
	for _, value := range splitGormTag {
		if strings.Contains(value, param) {
			paramValue := strings.Split(value, "=")
			if len(paramValue) == 1 {
				return ""
			}
			return paramValue[1]
		}
	}
	return ""
}

func IsParamInTagExists(tag reflect.StructTag, param string) bool {
	gormTag := tag.Get(core.GorganyORMTag)
	splitGormTag := strings.Split(gormTag, ",")
	for _, value := range splitGormTag {
		if strings.Contains(value, param) {
			return true
		}
	}
	return false
}

// GetTableName returns the table name for an entity, checking for custom TableName() method first,
// then falling back to the default naming strategy
func GetTableName(entity interface{}) string {
	if entity == nil {
		return ""
	}

	// Get the reflect value and type
	val := reflect.ValueOf(entity)
	originalVal := val

	// Check if the entity implements TableName() method on the pointer
	if val.Kind() == reflect.Ptr {
		// Check if the pointer is nil or zero value
		if val.IsNil() || val.IsZero() {
			// For nil/zero pointers, we can't call methods, so fall back to naming strategy
			indirectType := util.IndirectType(originalVal.Type())
			namer := schema.NamingStrategy{}
			return namer.TableName(indirectType.Name())
		}

		// Try to call TableName() on the pointer first
		tableNameMethod := val.MethodByName("TableName")
		if tableNameMethod.IsValid() {
			// Call the TableName() method
			results := tableNameMethod.Call(nil)
			if len(results) > 0 && results[0].Kind() == reflect.String {
				tableName := results[0].String()
				if tableName != "" {
					return tableName
				}
			}
		}
		// If not found on pointer, try on the value
		val = val.Elem()
	}

	// Check if the value is zero (uninitialized)
	if val.IsZero() {
		// For zero values, we can't call methods, so fall back to naming strategy
		indirectType := util.IndirectType(originalVal.Type())
		namer := schema.NamingStrategy{}
		return namer.TableName(indirectType.Name())
	}

	// Check if the entity implements TableName() method on the value
	tableNameMethod := val.MethodByName("TableName")
	if tableNameMethod.IsValid() {
		// Call the TableName() method
		results := tableNameMethod.Call(nil)
		if len(results) > 0 && results[0].Kind() == reflect.String {
			tableName := results[0].String()
			if tableName != "" {
				return tableName
			}
		}
	}

	// Fall back to default naming strategy
	indirectType := util.IndirectType(originalVal.Type())
	namer := schema.NamingStrategy{}
	return namer.TableName(indirectType.Name())
}
