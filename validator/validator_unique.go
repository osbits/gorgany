package validator

import (
	"context"
	"fmt"
	goValidator "github.com/go-playground/validator/v10"
	"github.com/osbits/gorgany/v2/db"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/service/cache"
	"github.com/osbits/gorgany/v2/util"
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

		dataSource := db.Connection()
		if dataSource == nil {
			// Fail closed. A uniqueness rule that cannot reach the database must not report
			// "available" — that is how a duplicate gets written.
			err2.HandleError(fmt.Errorf("validator: no default database connection, cannot check uniqueness of %s.%s", tabler.TableName(), columnName))
			return false
		}

		session, sessionErr := dataSource.NewSession()
		if sessionErr != nil {
			err2.HandleError(sessionErr)
			return false
		}
		defer session.Close()

		// The builder is copy-on-write: every clause method returns a new builder and leaves
		// the receiver untouched, so each clause has to be assigned back.
		builder := session.Query().From(tabler.TableName()).Eq(columnName, value.Interface())

		// Exclude the row being updated, or it collides with itself. Skipped for an entity
		// with no key yet, which is every insert.
		//
		// This was an OR against the primary key, which made the predicate
		// `column = value OR id != current` — true for essentially every row in the table, so
		// the count was never zero and nothing could ever validate as unique. It has to be an
		// AND: the value is taken only if a *different* row already holds it.
		for _, primary := range parsedDomain.PrimaryFields {
			val := util.IndirectValue(reflectParent).FieldByName(primary.Name)
			if !val.IsValid() {
				err2.HandleError(fmt.Errorf("Field %s(%s) is invalid", primary.Name, reflectParent.Type().Name()))
				return false
			}
			if val.IsZero() {
				continue
			}
			builder = builder.Neq(primary.DBName, val.Interface())
		}

		sql, args, sqlErr := builder.Select("COUNT(*)").ToSQL()
		if sqlErr != nil {
			err2.HandleError(sqlErr)
			return false
		}

		count, countErr := session.Executor().CountRaw(context.Background(), sql, args...)
		if countErr != nil {
			err2.HandleError(countErr)
			return false
		}

		return count == 0
	}

	return true
}
