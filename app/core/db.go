package core

import "gorm.io/gorm"

type DbType string

const (
	GormPostgreSQL DbType = "postgres_gorm"
	MongoDb        DbType = "mongo"
)

type IConnection interface {
	Driver() any
	Builder() IQueryBuilder
}

type IQueryBuilder interface {
	Select(fields ...string) IQueryBuilder
	From(table string) IQueryBuilder
	FromModel(model any) IQueryBuilder
	Join(table string, on string) IQueryBuilder
	WhereEqual(field string, value interface{}) IQueryBuilder
	Where(field string, operator string, value interface{}) IQueryBuilder
	WhereClosure(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	WhereIn(field string, values ...interface{}) IQueryBuilder
	WhereAnd(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	WhereOr(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	OrderBy(field string, direction string) IQueryBuilder
	Limit(limit int) IQueryBuilder
	Offset(offset int) IQueryBuilder
	BuildSelect() string
	BuildJoin() string
	BuildWhere() string
	BuildWhereAnd() string
	BuildWhereOr() string
	BuildOrder() string
	BuildLimit() string
	BuildOffset() string
	DeleteQuery() string
	ToQuery() string
	ToProcessedQuery() string
	Get(dest any) error
	Count(dest *int64) error
	List(dest any) error
	Insert(model any) error
	Save(model any) error
	Delete() error
	DeleteModel(model any) error
	StartTransaction() IQueryBuilder
	EndTransaction() IQueryBuilder
	RollbackTransaction() IQueryBuilder
	Relation(relation string) IQueryBuilder
	Raw(sql string, scan any, values ...any) error
	GetConnection() IConnection
	ReplaceRelation(relation string) error
	DeleteRelation(relation string) error
	ClearRelation(relation string) error
	AppendRelation(relation string, values ...any) error
	LoadRelations(relation ...string) error
	GetArgs() []any
	AddMetaToModel(dest any, statement *gorm.Statement)
}

type GormAssociation interface {
	Association(association string) *gorm.Association
}

type IOrm[T any] interface {
	Select(fields ...string) IOrm[T]
	Join(table string, on string) IOrm[T]
	WhereEqual(field string, value interface{}) IOrm[T]
	Where(field string, operator string, value interface{}) IOrm[T]
	WhereClosure(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	WhereIn(field string, values ...interface{}) IOrm[T]
	WhereAnd(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	WhereOr(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	OrderBy(field string, direction string) IOrm[T]
	Relation(relation string) IOrm[T]
	Limit(limit int) IOrm[T]
	Offset(offset int) IOrm[T]
	Get() (*T, error)
	Count() (int64, error)
	List() ([]*T, error)
	Save() error
	ReplaceRelation(relation string) error
	DeleteRelation(relation string) error
	ClearRelation(relation string) error
	AppendRelation(relation string, values ...any) error
	LoadRelations(relations ...string) error
	Delete() error
	ToQuery() string
}

type DbTyper interface {
	GetDbType() DbType
}
