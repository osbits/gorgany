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

// GetTableName returns the table name for an entity: what its TableName() method says, and
// otherwise the default naming strategy's name for its type.
//
// A live value is asked first, so a TableName that depends on the entity's own fields keeps
// working. A nil *T or a zero value used to skip the method and go straight to the naming
// strategy, and that is the shape the ORM hands this function when it has no entity yet:
// All, Count and Preload's loads pass `var sample T`, which for a pointer model is a nil *T.
// Every model whose TableName differs from the naming strategy's default was therefore read
// from a table that does not exist. The method is now called on a fresh zero value of the
// type instead, whichever receiver it has, which is exactly what gorm's schema.Parse does
// for the same model — so the two can no longer disagree about where a model lives.
func GetTableName(entity interface{}) string {
	if entity == nil {
		return ""
	}

	val := reflect.ValueOf(entity)
	entityType := util.IndirectType(val.Type())

	if val.Kind() == reflect.Ptr {
		if !val.IsNil() {
			// The pointer's method set holds both value and pointer receivers.
			if tableName := tableNameMethod(val); tableName != "" {
				return tableName
			}
		}
	} else if !val.IsZero() {
		if tableName := tableNameMethod(val); tableName != "" {
			return tableName
		}
		// A value does not carry its pointer-receiver methods, so ask an addressable copy.
		addressable := reflect.New(val.Type())
		addressable.Elem().Set(val)
		if tableName := tableNameMethod(addressable); tableName != "" {
			return tableName
		}
	}

	// A nil pointer or a zero value has no state of its own to name a table from, and a
	// zero value of the type answers the question exactly as well.
	if tableName := tableNameMethod(reflect.New(entityType)); tableName != "" {
		return tableName
	}

	namer := schema.NamingStrategy{}
	return namer.TableName(entityType.Name())
}

// tableNameMethod calls v's TableName() method and returns what it says, or "" when v has no
// such method or it is not the `TableName() string` gorm's schema.Tabler recognises. v must
// not be a nil pointer: a value-receiver method would dereference it.
func tableNameMethod(v reflect.Value) string {
	method := v.MethodByName("TableName")
	if !method.IsValid() {
		return ""
	}
	methodType := method.Type()
	if methodType.NumIn() != 0 || methodType.NumOut() != 1 || methodType.Out(0).Kind() != reflect.String {
		return ""
	}
	return method.Call(nil)[0].String()
}
