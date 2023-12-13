package validator

import (
	"git.qix.sx/gorgany/gorgany.git/db"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/util"
	goValidator "github.com/go-playground/validator/v10"
	"gorm.io/gorm/schema"
	"reflect"
	"unsafe"
)

func validateUnique(fieldLevel goValidator.FieldLevel) bool {
	reflectParent := fieldLevel.Parent()
	if reflectParent.Type().Kind() != reflect.Ptr {
		reflectParent = reflect.NewAt(reflectParent.Type(), unsafe.Pointer(reflectParent.UnsafeAddr()))
	}
	parent := reflectParent.Interface()
	if tabler, ok := parent.(schema.Tabler); ok {
		ns := schema.NamingStrategy{}
		columnName := ns.ColumnName(tabler.TableName(), fieldLevel.StructFieldName())
		value := fieldLevel.Field()

		if util.IndirectValue(value).IsZero() {
			return true
		}

		count := int64(0)
		err := db.Builder().From(tabler.TableName()).WhereEqual(columnName, value.Interface()).Count(&count)
		if err != nil {
			err2.HandleError(err)
			return false
		}

		return count == 0
	}

	return true
}
