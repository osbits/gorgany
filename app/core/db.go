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
	FromSubquery(table any) IQueryBuilder
	FromModel(model any) IQueryBuilder
	Join(table any, left, operator, right string) IQueryBuilder
	LeftJoin(table any, left, operator, right string) IQueryBuilder
	RightJoin(table any, left, operator, right string) IQueryBuilder
	FullJoin(table any, left, operator, right string) IQueryBuilder
	WhereEqual(field string, value interface{}) IQueryBuilder
	Where(field string, operator string, value interface{}) IQueryBuilder
	// Deprecated. Use WhereAnd instead
	WhereClosure(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	WhereIn(field string, values ...interface{}) IQueryBuilder
	WhereNotIn(field string, values ...interface{}) IQueryBuilder
	WhereAnd(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	WhereOr(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	OrderBy(field string, direction string) IQueryBuilder
	Limit(limit int) IQueryBuilder
	Offset(offset int) IQueryBuilder
	BuildSelect() string
	BuildJoin() (string, []any)
	BuildWhere() (string, []any)
	BuildOrder() string
	BuildLimit() string
	BuildOffset() string
	DeleteQuery() (string, []any)
	ToQuery() (string, []any)
	ToProcessedQuery() string
	Get(dest any) error
	Count(dest *int64) error
	List(dest any) error
	Insert(model any) error
	Save(model any) error
	Delete() error
	DeleteModel(model any) error
	StartTransaction() IQueryBuilder
	CommitTransaction() IQueryBuilder
	RollbackTransaction() IQueryBuilder
	Relation(relation string) IQueryBuilder
	Raw(sql string, scan any, values ...any) error
	GetConnection() IConnection
	CountRelation(relation string) (int64, error)
	ReplaceRelation(relation string, values ...any) error
	DeleteRelation(relation string) error
	ClearRelation(relation string) error
	AppendRelation(relation string, values ...any) error
	LoadRelations(relation ...string) error
	GetWhere() IWhere
	AddMetaToModel(dest any, statement *gorm.Statement)
	SetAlias(alias string) IQueryBuilder
	GetAlias() string
}

type GormAssociation interface {
	Association(association string) *gorm.Association
}

type IOrm[T any] interface {
	Select(fields ...string) IOrm[T]
	Join(table string, left, operator, right string) IOrm[T]
	LeftJoin(table string, left, operator, right string) IOrm[T]
	RightJoin(table string, left, operator, right string) IOrm[T]
	FullJoin(table string, left, operator, right string) IOrm[T]
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
	CountRelation(relation string) (int64, error)
	ReplaceRelation(relation string) error
	DeleteRelation(relation string) error
	ClearRelation(relation string) error
	AppendRelation(relation string, values ...any) error
	LoadRelations(relations ...string) error
	Delete() error
	ToQuery() string
}

type DbConnectionNamer interface {
	DbConnectionName() string
}

type IFrom interface {
	From(table any, alias string)
	ToQuery() (string, []any)
}

type IJoin interface {
	InnerJoin(table any, left, operator, right string)
	LeftJoin(table any, left, operator, right string)
	RightJoin(table any, left, operator, right string)
	FullJoin(table any, left, operator, right string)
	ToQuery() (string, []any)
}

type IWhere interface {
	AddCondition(column string, operator string, value any)
	AddNestedCondition(connectorOperator string, nestedWhere IWhere)
	ToQuery() (string, []any)
}
