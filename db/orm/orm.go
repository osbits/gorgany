package orm

import (
	"gorgany/app/core"
	"gorgany/db"
	"gorm.io/gorm"
	"reflect"
)

type GorganyOrm[T any] struct {
	builder core.IQueryBuilder
	Model   *T
}

func (thiz *GorganyOrm[T]) Select(fields ...string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Select(fields...)
	return thiz
}

func (thiz *GorganyOrm[T]) Join(table string, on string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, on)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereEqual(field string, value interface{}) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereEqual(field, value)
	return thiz
}

func (thiz *GorganyOrm[T]) Where(field string, operator string, value interface{}) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Where(field, operator, value)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereClosure(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereClosure(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereIn(field string, values ...interface{}) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereIn(field, values)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereAnd(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereAnd(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) WhereOr(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.WhereOr(closure)
	return thiz
}

func (thiz *GorganyOrm[T]) OrderBy(field string, direction string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.OrderBy(field, direction)
	return thiz
}

func (thiz *GorganyOrm[T]) Limit(limit int) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Limit(limit)
	return thiz
}

func (thiz *GorganyOrm[T]) Offset(offset int) core.IOrm[T] {
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

	return thiz.builder.FromModel(thiz.Model).(core.GormAssociation).Association(association)
}

func (thiz *GorganyOrm[T]) Relation(relation string) core.IOrm[T] {
	var model T
	thiz.setBuilder()

	thiz.builder.FromModel(model).Relation(relation)
	return thiz
}

func (thiz *GorganyOrm[T]) ReplaceRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.FromModel(thiz.Model).ReplaceRelation(relation)
}

func (thiz *GorganyOrm[T]) DeleteRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.FromModel(thiz.Model).DeleteRelation(relation)
}

func (thiz *GorganyOrm[T]) ClearRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.FromModel(thiz.Model).ClearRelation(relation)
}

func (thiz *GorganyOrm[T]) AppendRelation(relation string, values ...any) error {
	thiz.setBuilder()
	return thiz.builder.FromModel(thiz.Model).AppendRelation(relation)
}

func (thiz *GorganyOrm[T]) LoadRelations(relations ...string) error {
	thiz.setBuilder()
	return thiz.builder.FromModel(thiz.Model).LoadRelations(relations...)
}

func (thiz *GorganyOrm[T]) ToQuery() string {
	return thiz.builder.ToProcessedQuery()
}

func (thiz *GorganyOrm[T]) setBuilder() {
	if thiz.builder != nil {
		return
	}

	rv := reflect.ValueOf(thiz)
	if !rv.CanConvert(reflect.TypeOf((*core.DbTyper)(nil))) {
		thiz.builder = db.Builder(core.GormPostgreSQL) //todo read default dbType from config
		return
	}

	casted, ok := rv.Convert(reflect.TypeOf((*core.DbTyper)(nil))).Interface().(core.DbTyper)
	if !ok {
		thiz.builder = db.Builder(core.GormPostgreSQL)
		return
	}

	thiz.builder = db.Builder(casted.GetDbType())
	return
}
