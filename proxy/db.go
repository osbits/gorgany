package proxy

import "gorm.io/gorm"

type DbType string

const (
	GormPostgresQL DbType = "postgres_gorm"
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
}

type GormAssociation interface {
	Association(association string) *gorm.Association
}
