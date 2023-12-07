package postgres

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany/app/core"
	"gorgany/db/orm"
	"gorgany/util"
	"gorgany/validator"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"reflect"
	"strings"
	"sync"
)

const RecursiveRelationMaxDeep = 2

type GormPostgresConnection struct {
	gormInstance *gorm.DB
}

func (thiz *GormPostgresConnection) Driver() any {
	return thiz.gormInstance
}

func (thiz *GormPostgresConnection) Builder() core.IQueryBuilder {
	return NewBuilder(thiz)
}

type Config struct {
	PreloadingMaxDeep int
}

func (thiz Config) SetPreloadingMaxDeep(maxDeep int) {
	thiz.PreloadingMaxDeep = maxDeep
}

type Builder struct {
	gormInstance    core.IConnection
	config          Config
	selectStatement []string
	from            core.IFrom
	join            core.IJoin
	where           core.IWhere
	order           []string
	limit           *int
	offset          *int
	alias           string
	groupBy         []string
	having          core.IHaving

	copyGorm *gorm.DB
}

func NewBuilder(gormInstance core.IConnection) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       Config{PreloadingMaxDeep: RecursiveRelationMaxDeep},
		from:         &From{},
		join:         &Join{make(map[string][]JoinItems)},
		where:        &Where{operator: "AND"},
		having:       &Having{},
	}
}

func NewBuilderWithConfig(gormInstance core.IConnection, config Config) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       config,
		from:         &From{},
		join:         &Join{make(map[string][]JoinItems)},
		where:        &Where{operator: "AND"},
		having:       &Having{},
	}
}

func (thiz *Builder) Select(fields ...string) core.IQueryBuilder {
	thiz.selectStatement = append(thiz.selectStatement, fields...)
	return thiz
}

func (thiz *Builder) From(table string) core.IQueryBuilder {
	thiz.from.From(table, "")
	return thiz
}

func (thiz *Builder) FromSubquery(table any) core.IQueryBuilder {
	thiz.from.From(table, "")
	return thiz
}

// FromModel. It allows to use only for setting one (main) table
func (thiz *Builder) FromModel(model any) core.IQueryBuilder {
	thiz.copyGorm = &(*thiz.GetDriver())
	thiz.copyGorm = thiz.copyGorm.Model(model)

	namingStrategy := schema.NamingStrategy{}
	rtModel := reflect.TypeOf(model)

	if len(thiz.GetPostgresGORMFrom().fromItems) == 0 { // allow using FromModel only for specifying one (main) table
		if tabler, ok := model.(schema.Tabler); ok {
			thiz.From(tabler.TableName())
		} else {
			thiz.From(namingStrategy.TableName(util.IndirectType(rtModel).Name()))
		}
	}

	return thiz
}

func (thiz *Builder) Join(table any, left, operator, right string) core.IQueryBuilder {
	thiz.join.InnerJoin(table, left, operator, right)
	return thiz
}

func (thiz *Builder) LeftJoin(table any, left, operator, right string) core.IQueryBuilder {
	thiz.join.LeftJoin(table, left, operator, right)
	return thiz
}

func (thiz *Builder) RightJoin(table any, left, operator, right string) core.IQueryBuilder {
	thiz.join.RightJoin(table, left, operator, right)
	return thiz
}

func (thiz *Builder) FullJoin(table any, left, operator, right string) core.IQueryBuilder {
	thiz.join.FullJoin(table, left, operator, right)
	return thiz
}

func (thiz *Builder) WhereEqual(field string, value interface{}) core.IQueryBuilder {
	thiz.where.AddCondition(field, "=", value)
	return thiz
}

func (thiz *Builder) Where(field string, operator string, value interface{}) core.IQueryBuilder {
	thiz.where.AddCondition(field, operator, value)
	return thiz
}

func (thiz *Builder) WhereIn(field string, values ...interface{}) core.IQueryBuilder {
	thiz.where.AddCondition(field, "IN", values)
	return thiz
}

func (thiz *Builder) WhereNotIn(field string, values ...interface{}) core.IQueryBuilder {
	thiz.where.AddCondition(field, "NOT IN", values)
	return thiz
}

// WhereClosure. Deprecated. Use WhereAnd instead
func (thiz *Builder) WhereClosure(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	thiz.where.AddNestedCondition("AND", builder.GetWhere())
	return thiz
}

func (thiz *Builder) WhereAnd(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	nestedWhere := builder.GetWhere()
	if nestedWhere != nil {
		thiz.where.AddNestedCondition("AND", nestedWhere)
	}
	return thiz
}

func (thiz *Builder) WhereOr(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	nestedWhere := builder.GetWhere()
	if nestedWhere != nil {
		thiz.where.AddNestedCondition("OR", nestedWhere)
	}
	return thiz
}

func (thiz *Builder) Between(field string, firstValue, secondValue any) core.IQueryBuilder {
	thiz.where.AddCondition(field, "BETWEEN", Between{
		firstValue:  firstValue,
		secondValue: secondValue,
	})
	return thiz
}

func (thiz *Builder) OrderBy(field string, direction string) core.IQueryBuilder {
	thiz.order = append(thiz.order, field+" "+direction)
	return thiz
}

func (thiz *Builder) Limit(limit int) core.IQueryBuilder {
	thiz.limit = &limit
	return thiz
}

func (thiz *Builder) Offset(offset int) core.IQueryBuilder {
	thiz.offset = &offset
	return thiz
}

func (thiz *Builder) GroupBy(groupBy string) core.IQueryBuilder {
	thiz.groupBy = append(thiz.groupBy, groupBy)
	return thiz
}

func (thiz *Builder) Having(rawStatement string, operator string, value any) core.IQueryBuilder {
	thiz.having.AddItem(rawStatement, operator, value)
	return thiz
}

func (thiz *Builder) DeleteQuery() (string, []any) {
	where, args := thiz.BuildWhere()
	if viper.GetBool("databases.postgres_gorm.log") == true {
		fmt.Printf("DELETE FROM %s %s\n", thiz.from, where)
	}
	return fmt.Sprintf("DELETE FROM %s %s", thiz.from, where), args
}

func (thiz *Builder) ToQuery() (string, []any) {
	from, fromArgs := thiz.BuildFrom()
	join, joinArgs := thiz.BuildJoin()
	where, whereArgs := thiz.BuildWhere()
	having, havingArgs := thiz.BuildHaving()
	args := append(fromArgs, joinArgs...)
	args = append(args, whereArgs...)
	args = append(args, havingArgs...)
	return fmt.Sprintf("SELECT %s FROM %s%s%s%s%s%s%s%s", thiz.BuildSelect(), from, join, where, thiz.BuildOrder(), thiz.BuildLimit(), thiz.BuildOffset(), thiz.BuildGroupBy(), having), args
}

func (thiz *Builder) ToProcessedQuery() string {
	return thiz.GetDriver().ToSQL(func(g *gorm.DB) *gorm.DB {
		query, args := thiz.ToQuery()
		return g.Raw(query, args...)
	})
}

func (thiz *Builder) BuildSelect() string {
	if len(thiz.selectStatement) == 0 {
		return "*"
	}
	return strings.Join(thiz.selectStatement, ", ")
}

func (thiz *Builder) BuildFrom() (string, []any) {
	if len(thiz.GetPostgresGORMFrom().fromItems) == 0 {
		return "", nil
	}
	buildFrom, args := thiz.from.ToQuery()
	return buildFrom, args
}

func (thiz *Builder) BuildJoin() (string, []any) {
	if len(thiz.GetPostgresGORMJoin().joinItems) == 0 {
		return "", nil
	}
	builtJoin, args := thiz.join.ToQuery()
	return " " + builtJoin, args
}

func (thiz *Builder) BuildWhere() (string, []any) {
	if len(thiz.GetPostgresGORMWhere().whereItems) == 0 {
		return "", nil
	}
	builtWhere, args := thiz.where.ToQuery()
	return " WHERE " + builtWhere, args
}

func (thiz *Builder) BuildOrder() string {
	if len(thiz.order) == 0 {
		return ""
	}
	return " ORDER BY " + strings.Join(thiz.order, ",")
}

func (thiz *Builder) BuildLimit() string {
	if thiz.limit == nil {
		return ""
	}
	return fmt.Sprintf(" LIMIT %d", *thiz.limit)
}

func (thiz *Builder) BuildOffset() string {
	if thiz.offset == nil {
		return ""
	}
	return fmt.Sprintf(" OFFSET %d", *thiz.offset)
}

func (thiz *Builder) BuildGroupBy() string {
	if len(thiz.groupBy) == 0 {
		return ""
	}
	return fmt.Sprintf(" GROUP BY ", strings.Join(thiz.groupBy, ","))
}

func (thiz *Builder) BuildHaving() (string, []any) {
	if len(thiz.GetPostgresGORMHaving().havingItems) == 0 {
		return "", nil
	}
	builtHaving, args := thiz.having.ToQuery()
	return " HAVING " + builtHaving, args
}

func (thiz *Builder) Get(dest any) error {
	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr {
		return fmt.Errorf("Dest must be pointer")
	}

	sc, err := schema.Parse(dest, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return err
	}

	if len(thiz.GetPostgresGORMFrom().fromItems) == 0 {
		thiz.FromModel(rvDest.Elem().Interface())
	}

	keys := thiz.walkNestedRelations(sc.Relationships.Relations, "", 0)
	for _, key := range keys {
		thiz.Relation(key)
	}

	query, args := thiz.ToQuery()
	res := thiz.GetDriver().Raw(query, args...).First(dest)
	thiz.AddMetaToModel(dest, res.Statement)

	thiz.clearQueryParams()

	if res.Error != nil && res.Error.Error() == "record not found" {
		return nil
	}
	return res.Error
}

func (thiz *Builder) Count(dest *int64) error {
	thiz.Select("count(*)")
	query, args := thiz.ToQuery()
	res := thiz.GetDriver().Raw(query, args...).Scan(dest)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) List(dest any) error {
	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr {
		return fmt.Errorf("Dest must be pointer")
	}
	rvDestSlice := rvDest.Elem()
	if rvDestSlice.Kind() != reflect.Slice {
		return fmt.Errorf("Dest must be slice")
	}

	model := util.GetElementOfSlice(rvDestSlice.Interface())
	thiz.FromModel(model)

	sc, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return err
	}

	keys := thiz.walkNestedRelations(sc.Relationships.Relations, "", 0)
	for _, key := range keys {
		thiz.Relation(key)
	}

	query, args := thiz.ToQuery()
	res := thiz.GetDriver().Raw(query, args...).Find(dest)
	thiz.clearQueryParams()

	for _, d := range util.GetSliceFromAny(dest) {
		thiz.AddMetaToModel(d, res.Statement)
	}

	if res.Error != nil && res.Error.Error() == "record not found" {
		return nil
	}
	return res.Error
}

func (thiz *Builder) Insert(model any) error {
	rvScan := reflect.ValueOf(model)
	if rvScan.Kind() != reflect.Ptr {
		return fmt.Errorf("Dest must be pointer")
	}

	res := thiz.GetDriver().Create(model)
	thiz.AddMetaToModel(model, res.Statement)

	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Save(model any) error {
	err := validator.ValidateStruct(model)
	if err != nil {
		return err
	}

	res := thiz.GetDriver().Save(model)
	thiz.AddMetaToModel(model, res.Statement)

	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Delete() error {
	query, args := thiz.DeleteQuery()
	res := thiz.GetDriver().Raw(query, args...)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) DeleteModel(model any) error {
	res := thiz.GetDriver().Delete(model)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Relation(relation string) core.IQueryBuilder {
	if thiz.copyGorm == nil {
		panic("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	thiz.copyGorm = thiz.copyGorm.Preload(relation)
	return thiz
}

func (thiz *Builder) GetConnection() core.IConnection {
	return thiz.gormInstance
}

func (thiz *Builder) GetDriver() *gorm.DB {
	if thiz.copyGorm == nil {
		return thiz.GetConnection().Driver().(*gorm.DB)
	}
	return thiz.copyGorm
}

func (thiz *Builder) StartTransaction() core.IQueryBuilder {
	thiz.copyGorm = thiz.GetDriver().Begin()
	return thiz
}

func (thiz *Builder) CommitTransaction() core.IQueryBuilder {
	thiz.copyGorm = thiz.GetDriver().Commit()
	thiz.clearQueryParams()
	return thiz
}

func (thiz *Builder) RollbackTransaction() core.IQueryBuilder {
	thiz.copyGorm = thiz.GetDriver().Rollback()
	thiz.clearQueryParams()
	return thiz
}

func (thiz *Builder) Raw(sql string, scan any, values ...any) error {
	res := thiz.GetDriver().Raw(sql, values...)

	rvScan := reflect.ValueOf(scan)
	if rvScan.Kind() != reflect.Ptr { //nil ??
		return fmt.Errorf("Scan must be pointer")
	}

	if scan != nil {
		res.Scan(scan)
	}

	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Association(association string) *gorm.Association {
	if thiz.copyGorm == nil {
		panic("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(association)
}

func (thiz *Builder) CountRelation(relation string) (int64, error) {
	if thiz.copyGorm == nil {
		return 0, fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Count(), nil
}

func (thiz *Builder) ReplaceRelation(relation string, values ...any) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Replace(values...)
}

func (thiz *Builder) DeleteRelation(relation string, values ...any) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Delete(values...)
}

func (thiz *Builder) ClearRelation(relation string) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Clear()
}

func (thiz *Builder) AppendRelation(relation string, values ...any) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Append(values...)
}

func (thiz *Builder) LoadRelations(relations ...string) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}

	for _, relation := range relations {
		splitRelation := strings.Split(relation, ".")

		r := splitRelation[0]
		rv := reflect.ValueOf(thiz.copyGorm.Statement.Model)
		rvField := rv.Elem().FieldByName(r)

		fieldType := util.IndirectType(rvField.Type())
		fieldValue := reflect.New(fieldType)
		v := fieldValue.Interface()

		err := thiz.copyGorm.Association(r).Find(&v)
		if err != nil {
			return err
		}
		thiz.AddMetaToModel(v, thiz.copyGorm.Statement)

		if len(splitRelation) > 1 {
			thiz.clearQueryParams()
			err := thiz.FromModel(v).LoadRelations(strings.Join(splitRelation[1:], "."))
			if err != nil {
				return err
			}
		}

		rvField.Set(reflect.ValueOf(v))
	}
	return nil
}

func (thiz *Builder) clearQueryParams() {
	thiz.selectStatement = make([]string, 0)
	thiz.where = &Where{operator: "AND"}
	thiz.from = &From{}
	thiz.join = &Join{make(map[string][]JoinItems)}
	thiz.limit = nil
	thiz.order = make([]string, 0)
	thiz.offset = nil
	thiz.groupBy = make([]string, 0)
	thiz.having = &Having{}
	thiz.copyGorm = nil
}

func (thiz *Builder) GetWhere() core.IWhere {
	return thiz.where
}

func (thiz *Builder) AddMetaToModel(dest any, statement *gorm.Statement) {
	domainMetaInstance, ok := dest.(core.IDomainMeta)
	if ok {
		domainMetaInstance.SetLoaded(true)
		domainMetaInstance.SetTable(statement.Table)
		domainMetaInstance.SetDriver(core.GormPostgreSQL)

		copyDest := util.IndirectValue(reflect.ValueOf(dest)).Interface()
		domainMetaInstance.SetOriginal(&copyDest)

		domainMetaInstance.SetDomain(dest)
	}
}

func (thiz *Builder) SetAlias(alias string) core.IQueryBuilder {
	thiz.alias = alias
	return thiz
}

func (thiz *Builder) GetAlias() string {
	return thiz.alias
}

func (thiz *Builder) GetPostgresGORMFrom() *From {
	return thiz.from.(*From)
}

func (thiz *Builder) GetPostgresGORMJoin() *Join {
	return thiz.join.(*Join)
}

func (thiz *Builder) GetPostgresGORMWhere() *Where {
	return thiz.where.(*Where)
}

func (thiz *Builder) GetPostgresGORMHaving() *Having {
	return thiz.having.(*Having)
}

func (thiz *Builder) value(value interface{}) string {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return fmt.Sprintf("%v", value)
	default:
		val := fmt.Sprintf("%v", value)
		val = strings.ReplaceAll(val, "'", "''")
		return fmt.Sprintf("'%s'", val)
	}
}

func (thiz *Builder) values(values ...interface{}) string {
	var result string
	for _, value := range values {
		result += thiz.value(value) + ","
	}
	return result[:len(result)-1]
}

func (thiz *Builder) walkNestedRelations(relations map[string]*schema.Relationship, parentRelationPath string, depth int) []string {
	keys := make([]string, 0)

	for name, nestedRelation := range relations {
		preload := orm.IsParamInTagExists(nestedRelation.Field.Tag, core.GorganyORMPreload)
		if !preload {
			continue
		}

		relationPath := name
		if parentRelationPath != "" {
			relationPath = parentRelationPath + "." + name
		}
		keys = append(keys, relationPath)

		if depth < thiz.config.PreloadingMaxDeep {
			keys = append(keys, thiz.walkNestedRelations(nestedRelation.FieldSchema.Relationships.Relations, relationPath, depth+1)...)
		}
	}

	return keys
}
