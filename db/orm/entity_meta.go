package orm

import (
	"time"

	dbCore "github.com/osbits/gorgany/db/sql/core"
)

// RelationMeta stores metadata about a loaded relation
type RelationMeta struct {
	Type       string
	ForeignKey string
	JoinTable  string
	LoadedAt   time.Time
	Count      int
	Extra      map[string]interface{}
}

// EntityMeta stores metadata about an domain
type EntityMeta struct {
	TableName     string
	PrimaryKey    string
	DatabaseName  string
	IsLoaded      bool
	IsDirty       bool
	LoadedColumns map[string]bool
	LastQuery     string
	LastArgs      []interface{}
	QueryBuilder  dbCore.IQueryBuilder
	QueryResult   *dbCore.QueryResult
	RelationMeta  map[string]*RelationMeta
	DataSource    dbCore.IDataSource
}

func (e *EntityMeta) IsRelationLoaded(relationName string) bool {
	return e.RelationMeta != nil && e.RelationMeta[relationName] != nil
}

func (e *EntityMeta) SetRelationLoaded(relationName string, meta *RelationMeta) {
	if e.RelationMeta == nil {
		e.RelationMeta = make(map[string]*RelationMeta)
	}
	e.RelationMeta[relationName] = meta
}

func (e *EntityMeta) GetRelationMeta(relationName string) *RelationMeta {
	if e.RelationMeta == nil {
		return nil
	}
	return e.RelationMeta[relationName]
}

func (e *EntityMeta) SetQueryResult(result *dbCore.QueryResult) {
	e.QueryResult = result
	e.IsLoaded = result.Found
}

func (e *EntityMeta) GetQueryResult() *dbCore.QueryResult {
	return e.QueryResult
}

// EntityWithMeta is an interface that all entities with metadata must implement
type EntityWithMeta interface {
	GetMeta() *EntityMeta
	SetMeta(*EntityMeta)
}

// BaseEntity provides the Meta field and implementation of EntityWithMeta
type BaseEntity struct {
	Meta *EntityMeta `gorm:"-"`
}

func (e *BaseEntity) GetMeta() *EntityMeta {
	if e.Meta == nil {
		e.Meta = &EntityMeta{
			PrimaryKey:    "id",
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
		}
	}
	return e.Meta
}

func (e *BaseEntity) SetMeta(meta *EntityMeta) {
	e.Meta = meta
}
