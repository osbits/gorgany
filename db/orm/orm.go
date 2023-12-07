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

func OrmInstance[T any](model *T) *GorganyOrm[T] {
	if model == nil {
		var m T
		model = &m
	}
	orm := &GorganyOrm[T]{Model: model}
	orm.setBuilder()

	return orm
}

func (thiz *GorganyOrm[T]) Select(fields ...string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Select(fields...)
	return thiz
}

func (thiz *GorganyOrm[T]) Join(table string, left, operator, right string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, left, operator, right)
	return thiz
}

func (thiz *GorganyOrm[T]) LeftJoin(table string, left, operator, right string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, left, operator, right)
	return thiz
}

func (thiz *GorganyOrm[T]) RightJoin(table string, left, operator, right string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, left, operator, right)
	return thiz
}

func (thiz *GorganyOrm[T]) FullJoin(table string, left, operator, right string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Join(table, left, operator, right)
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

func (thiz *GorganyOrm[T]) Between(field string, firstValue any, secondValue any) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Between(field, firstValue, secondValue)
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

func (thiz *GorganyOrm[T]) GroupBy(field string) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.GroupBy(field)
	return thiz
}

func (thiz *GorganyOrm[T]) Having(rawStatement string, operator string, value any) core.IOrm[T] {
	thiz.setBuilder()
	thiz.builder.Having(rawStatement, operator, value)
	return thiz
}

func (thiz *GorganyOrm[T]) Get() (*T, error) {
	thiz.setBuilder()
	err := thiz.builder.Get(thiz.Model)
	return thiz.Model, err
}

func (thiz *GorganyOrm[T]) Count() (int64, error) {
	thiz.setBuilder()

	var count int64
	err := thiz.builder.Count(&count)
	return count, err
}

func (thiz *GorganyOrm[T]) List() ([]*T, error) {
	thiz.setBuilder()
	domains := make([]*T, 0)
	err := thiz.builder.List(&domains)
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

	return thiz.builder.(core.GormAssociation).Association(association)
}

func (thiz *GorganyOrm[T]) Relation(relation string) core.IOrm[T] {
	thiz.setBuilder()

	thiz.builder.Relation(relation)
	return thiz
}

func (thiz *GorganyOrm[T]) CountRelation(relation string) (int64, error) {
	thiz.setBuilder()
	return thiz.builder.CountRelation(relation)
}

func (thiz *GorganyOrm[T]) ReplaceRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.ReplaceRelation(relation)
}

func (thiz *GorganyOrm[T]) DeleteRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.DeleteRelation(relation)
}

func (thiz *GorganyOrm[T]) ClearRelation(relation string) error {
	thiz.setBuilder()
	return thiz.builder.ClearRelation(relation)
}

func (thiz *GorganyOrm[T]) AppendRelation(relation string, values ...any) error {
	thiz.setBuilder()
	return thiz.builder.AppendRelation(relation, values...)
}

func (thiz *GorganyOrm[T]) LoadRelations(relations ...string) error {
	thiz.setBuilder()
	return thiz.builder.LoadRelations(relations...)
}

func (thiz *GorganyOrm[T]) ToQuery() string {
	return thiz.builder.ToProcessedQuery()
}

func (thiz *GorganyOrm[T]) setBuilder() {
	if thiz.builder != nil {
		return
	}

	rv := reflect.ValueOf(thiz)
	if !rv.CanConvert(reflect.TypeOf((*core.DbConnectionNamer)(nil))) {
		thiz.builder = db.Builder()
		thiz.builder.FromModel(thiz.Model)
		return
	}

	casted, ok := rv.Convert(reflect.TypeOf((*core.DbConnectionNamer)(nil))).Interface().(core.DbConnectionNamer)
	if !ok {
		thiz.builder = db.Builder()
		thiz.builder.FromModel(thiz.Model)
		return
	}

	thiz.builder = db.Builder(casted.DbConnectionName())
	thiz.builder.FromModel(thiz.Model)
	return
}
