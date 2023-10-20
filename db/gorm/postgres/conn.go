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
	table           string
	join            []string
	where           []string
	order           []string
	args            []any
	limit           *int
	offset          *int
	copyGorm        *gorm.DB
}

func NewBuilder(gormInstance core.IConnection) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       Config{PreloadingMaxDeep: RecursiveRelationMaxDeep},
	}
}

func NewBuilderWithConfig(gormInstance core.IConnection, config Config) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       config,
	}
}

func (thiz *Builder) Select(fields ...string) core.IQueryBuilder {
	thiz.selectStatement = append(thiz.selectStatement, fields...)
	return thiz
}

func (thiz *Builder) From(table string) core.IQueryBuilder {
	thiz.table = table
	return thiz
}

func (thiz *Builder) FromModel(model any) core.IQueryBuilder {
	thiz.copyGorm = &(*thiz.GetDriver())
	thiz.copyGorm = thiz.copyGorm.Model(model)

	namingStrategy := schema.NamingStrategy{}
	rtModel := reflect.TypeOf(model)

	if tabler, ok := model.(schema.Tabler); ok {
		thiz.From(tabler.TableName())
	} else {
		thiz.From(namingStrategy.TableName(rtModel.Name()))
	}

	return thiz
}

func (thiz *Builder) Join(table string, on string) core.IQueryBuilder {
	thiz.join = append(thiz.join, fmt.Sprintf("JOIN %s ON %s", table, on))
	return thiz
}

func (thiz *Builder) WhereEqual(field string, value interface{}) core.IQueryBuilder {
	thiz.where = append(thiz.where, field+" = ?")
	thiz.args = append(thiz.args, value)
	return thiz
}

func (thiz *Builder) Where(field string, operator string, value interface{}) core.IQueryBuilder {
	thiz.where = append(thiz.where, field+" "+operator+" ?")
	thiz.args = append(thiz.args, value)
	return thiz
}

func (thiz *Builder) WhereClosure(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	thiz.where = append(thiz.where, builder.BuildWhereAnd())
	return thiz
}

func (thiz *Builder) WhereIn(field string, values ...interface{}) core.IQueryBuilder {
	placeholders := make([]string, len(values))
	for i := 0; i < len(values); i++ {
		placeholders[i] = "?"
	}
	thiz.where = append(thiz.where, field+" IN ("+strings.Join(placeholders, ",")+")")
	thiz.args = append(thiz.args, values...)
	return thiz
}

func (thiz *Builder) WhereAnd(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	builtWhere := builder.BuildWhere()
	if builtWhere != "" {
		thiz.where = append(thiz.where, "("+builder.BuildWhereAnd()+")")
	}
	thiz.args = append(thiz.args, builder.GetArgs()...)
	return thiz
}

func (thiz *Builder) WhereOr(closure func(builder core.IQueryBuilder) core.IQueryBuilder) core.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	builtWhere := builder.BuildWhereOr()
	if builtWhere != "" {
		thiz.where = append(thiz.where, "("+builder.BuildWhereOr()+")")
	}
	thiz.args = append(thiz.args, builder.GetArgs()...)
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

func (thiz *Builder) DeleteQuery() string {
	if viper.GetBool("databases.postgres_gorm.log") == true {
		fmt.Printf("DELETE FROM %s %s\n", thiz.table, thiz.BuildWhere())
	}
	return fmt.Sprintf("DELETE FROM %s %s\n", thiz.table, thiz.BuildWhere())
}

func (thiz *Builder) ToQuery() string {
	if viper.GetBool("databases.postgres_gorm.log") == true {
		fmt.Printf("SELECT %s FROM %s %s %s %s %s %s\n", thiz.BuildSelect(), thiz.table, thiz.BuildJoin(), thiz.BuildWhere(), thiz.BuildOrder(), thiz.BuildLimit(), thiz.BuildOffset())
	}
	return fmt.Sprintf("SELECT %s FROM %s %s %s %s %s %s", thiz.BuildSelect(), thiz.table, thiz.BuildJoin(), thiz.BuildWhere(), thiz.BuildOrder(), thiz.BuildLimit(), thiz.BuildOffset())
}

func (thiz *Builder) ToProcessedQuery() string {
	return thiz.GetDriver().ToSQL(func(g *gorm.DB) *gorm.DB {
		return g.Raw(thiz.ToQuery(), thiz.args...)
	})
}

func (thiz *Builder) BuildSelect() string {
	if len(thiz.selectStatement) == 0 {
		return "*"
	}
	return strings.Join(thiz.selectStatement, ",")
}

func (thiz *Builder) BuildWhere() string {
	if len(thiz.where) == 0 {
		return ""
	}
	return "WHERE " + thiz.BuildWhereAnd()
}

func (thiz *Builder) BuildWhereOr() string {
	if len(thiz.where) == 0 {
		return ""
	}
	return strings.Join(thiz.where, " OR ")
}

func (thiz *Builder) BuildWhereAnd() string {
	if len(thiz.where) == 0 {
		return ""
	}
	return strings.Join(thiz.where, " AND ")
}

func (thiz *Builder) BuildJoin() string {
	return strings.Join(thiz.join, " ")
}

func (thiz *Builder) BuildOrder() string {
	if len(thiz.order) == 0 {
		return ""
	}
	return "ORDER BY " + strings.Join(thiz.order, ",")
}

func (thiz *Builder) BuildLimit() string {
	if thiz.limit == nil {
		return ""
	}
	return fmt.Sprintf("LIMIT %d", *thiz.limit)
}

func (thiz *Builder) BuildOffset() string {
	if thiz.offset == nil {
		return ""
	}
	return fmt.Sprintf("OFFSET %d", *thiz.offset)
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

	thiz.FromModel(rvDest.Elem().Interface())
	keys := thiz.walkNestedRelations(sc.Relationships.Relations, "", 0)
	for _, key := range keys {
		thiz.Relation(key)
	}

	res := thiz.GetDriver().Raw(thiz.ToQuery(), thiz.args...).First(dest)
	thiz.AddMetaToModel(dest, res.Statement)

	thiz.clearQueryParams()

	if res.Error != nil && res.Error.Error() == "record not found" {
		return nil
	}
	return res.Error
}

func (thiz *Builder) Count(dest *int64) error {
	thiz.Select("count(*)")
	res := thiz.GetDriver().Raw(thiz.ToQuery(), thiz.args...).Scan(dest)
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

	res := thiz.GetDriver().Raw(thiz.ToQuery(), thiz.args...).Find(dest)
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
	res := thiz.GetDriver().Raw(thiz.DeleteQuery(), thiz.args...)
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
	thiz.GetDriver().Begin()
	return thiz
}

func (thiz *Builder) EndTransaction() core.IQueryBuilder {
	thiz.GetDriver().Commit()
	thiz.clearQueryParams()
	return thiz
}

func (thiz *Builder) RollbackTransaction() core.IQueryBuilder {
	thiz.GetDriver().Rollback()
	thiz.clearQueryParams()
	return thiz
}

func (thiz *Builder) Raw(sql string, scan any, values ...any) error {
	res := thiz.GetDriver().Raw(sql, values)

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

func (thiz *Builder) ReplaceRelation(relation string) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Replace()
}

func (thiz *Builder) DeleteRelation(relation string) error {
	if thiz.copyGorm == nil {
		return fmt.Errorf("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	return thiz.copyGorm.Association(relation).Delete()
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
	return thiz.copyGorm.Association(relation).Append(values)
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
	thiz.where = make([]string, 0)
	thiz.table = ""
	thiz.join = make([]string, 0)
	thiz.limit = nil
	thiz.order = make([]string, 0)
	thiz.offset = nil
	thiz.copyGorm = nil
}

func (thiz *Builder) GetArgs() []any {
	return thiz.args
}

func (thiz *Builder) AddMetaToModel(dest any, statement *gorm.Statement) {
	domainMetaInstance, ok := dest.(core.IDomainMeta)
	if ok {
		domainMetaInstance.SetLoaded(true)
		domainMetaInstance.SetTable(statement.Table)
		domainMetaInstance.SetDb(statement.Schema.Name)
		domainMetaInstance.SetDriver(core.GormPostgreSQL)
	}
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
