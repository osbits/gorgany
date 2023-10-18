package orm

import (
	"gorgany/db"
	"gorgany/proxy"
	"gorm.io/gorm"
	"reflect"
)

type GorganyOrm[T any] struct {
	builder proxy.IQueryBuilder
	Model   *T
}

func (thiz *GorganyOrm[T]) Select(fields ...string) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Select(fields...)
	return thiz
}

func (thiz *GorganyOrm[T]) Join(table string, on string) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, on)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereEqual(field string, value interface{}) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereEqual(field, value)
	return thiz
}

func (thiz *GorganyOrm[T]) Where(field string, operator string, value interface{}) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Where(field, operator, value)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereClosure(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereClosure(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereIn(field string, values ...interface{}) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereIn(field, values)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereAnd(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereAnd(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereOr(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereOr(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) OrderBy(field string, direction string) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.OrderBy(field, direction)
	return thiz
}

func (thiz *GorganyOrm[T]) Limit(limit int) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Limit(limit)
	return thiz
}

func (thiz *GorganyOrm[T]) Offset(offset int) proxy.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Offset(offset)
	return thiz
}

func (thiz *GorganyOrm[T]) Get() (*T, error) {
	thiz.setBuilder()
	err := thiz.builder.Get(thiz.Model)
	return thiz.Model, err
}

func (thiz *GorganyOrm[T]) Count() (int64, error) {
	var model T

	thiz.setBuilder()

	var count int64
	err := thiz.builder.FromModel(model).Count(&count)
	return count, err
}

func (thiz *GorganyOrm[T]) List() ([]*T, error) {
	var model T

	thiz.setBuilder()
	domains := make([]*T, 0)
	err := thiz.builder.FromModel(model).List(&domains)
	return domains, err
}

func (thiz *GorganyOrm[T]) Save() error {
	thiz.setBuilder()
	return thiz.builder.Save(thiz.Model)
}

func (thiz *GorganyOrm[T]) Delete() error {
	thiz.setBuilder()
	return thiz.builder.DeleteModel(thiz.Model)
}

func (thiz *GorganyOrm[T]) Association(association string) *gorm.Association {
	thiz.setBuilder()

	return thiz.builder.FromModel(thiz.Model).(proxy.GormAssociation).Association(association)
}

func (thiz *GorganyOrm[T]) Relation(relation string) proxy.IOrm[T] {
	var model T
	thiz.setBuilder()

	thiz.builder.FromModel(model).Relation(relation)
	return thiz
}

func (thiz *GorganyOrm[T]) ToQuery() string {
	return thiz.builder.ToProcessedQuery()
}

func (thiz *GorganyOrm[T]) setBuilder() {
	if thiz.builder != nil {
		return
	}

	rv := reflect.ValueOf(thiz)
	if !rv.CanConvert(reflect.TypeOf((*proxy.DbTyper)(nil))) {
		thiz.builder = db.Builder(proxy.GormPostgresQL) //todo read default dbType from config
		return
	}

	casted, ok := rv.Convert(reflect.TypeOf((*proxy.DbTyper)(nil))).Interface().(proxy.DbTyper)
	if !ok {
		thiz.builder = db.Builder(proxy.GormPostgresQL)
		return
	}

	thiz.builder = db.Builder(casted.GetDbType())
	return
}
