package validator

import (
	"fmt"
	goValidator "github.com/go-playground/validator/v10"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db"
	err2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/service/cache"
	"github.com/osbits/gorgany/util"
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

		parsedDomain := cache.GetDomainSchemeCache().ParseDomain(parent)
		if parent == nil {
			err2.HandleError("DomainSchemeCache has return an empty parsed domain")
			return false
		}

		builder := db.Builder().From(tabler.TableName()).WhereEqual(columnName, value.Interface())

		var primaryCondition func(b core.IQueryBuilder) core.IQueryBuilder
		for _, primary := range parsedDomain.PrimaryFields {
			val := util.IndirectValue(reflectParent).FieldByName(primary.Name)
			if !val.IsValid() {
				err2.HandleError(fmt.Errorf("Field %s(%s) is invalid", primary.Name, reflectParent.Type().Name()))
				return false
			}
			primaryCondition = func(b core.IQueryBuilder) core.IQueryBuilder {
				b.Where(primary.DBName, "!=", val.Interface())
				return b
			}
		}

		count := int64(0)
		err := builder.WhereOr(primaryCondition).Count(&count)
		if err != nil {
			err2.HandleError(err)
			return false
		}

		return count == 0
	}

	return true
}
