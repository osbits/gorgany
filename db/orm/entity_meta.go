package orm

import (
	"time"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
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

	// DirtyColumns, when non-empty, restricts an UPDATE's SET list to these columns.
	//
	// Nil or empty means "write every column", which is what every caller before this did and
	// what Save still does — so an entity that never sets it is unaffected. Only updateEntity
	// consults it; an INSERT writes the whole row by definition, and the primary key is never
	// skipped because it is the WHERE, not a SET.
	//
	// It exists because a full-row UPDATE writes columns the caller never touched, using
	// whatever the in-memory copy happens to hold. For an entity two processes share — a
	// session row is the one the framework ships — that turns a routine heartbeat into a
	// silent overwrite of state the other process changed.
	DirtyColumns map[string]bool

	// UpdateGuard is ANDed into an UPDATE's WHERE on top of the primary key. It is how a
	// caller says "only if the row still looks the way I read it".
	//
	// A guarded statement that matches nothing, against a row that is still there, is
	// ErrRowConflict — a different answer from ErrRowGone, and callers act on them
	// differently: gone means fail closed, conflict means re-read and reconcile.
	UpdateGuard []dbCore.Condition
}

// isDirtyColumn reports whether an UPDATE should carry this column.
//
// An empty DirtyColumns set means "all of them", so the default is the pre-existing
// full-row behaviour and only an entity that opts in narrows its writes.
func (e *EntityMeta) isDirtyColumn(columnName string) bool {
	if len(e.DirtyColumns) == 0 {
		return true
	}
	return e.DirtyColumns[columnName]
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
