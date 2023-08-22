package postgres

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany/proxy"
	"gorgany/util"
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

func (thiz *GormPostgresConnection) Builder() proxy.IQueryBuilder {
	return NewBuilder(thiz)
}

type Config struct {
	PreloadingMaxDeep int
}

func (thiz Config) SetPreloadingMaxDeep(maxDeep int) {
	thiz.PreloadingMaxDeep = maxDeep
}

type Builder struct {
	gormInstance    proxy.IConnection
	config          Config
	selectStatement []string
	table           string
	join            []string
	where           []string
	order           []string
	limit           *int
	offset          *int
	copyGorm        *gorm.DB
}

func NewBuilder(gormInstance proxy.IConnection) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       Config{PreloadingMaxDeep: RecursiveRelationMaxDeep},
	}
}

func NewBuilderWithConfig(gormInstance proxy.IConnection, config Config) *Builder {
	return &Builder{
		gormInstance: gormInstance,
		config:       config,
	}
}

func (thiz *Builder) Select(fields ...string) proxy.IQueryBuilder {
	thiz.selectStatement = append(thiz.selectStatement, fields...)
	return thiz
}

func (thiz *Builder) From(table string) proxy.IQueryBuilder {
	thiz.table = table
	return thiz
}

func (thiz *Builder) FromModel(model any) proxy.IQueryBuilder {
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

func (thiz *Builder) Join(table string, on string) proxy.IQueryBuilder {
	thiz.join = append(thiz.join, fmt.Sprintf("JOIN %s ON %s", table, on))
	return thiz
}

func (thiz *Builder) WhereEqual(field string, value interface{}) proxy.IQueryBuilder {
	thiz.where = append(thiz.where, field+" = "+thiz.value(value))
	return thiz
}

func (thiz *Builder) Where(field string, operator string, value interface{}) proxy.IQueryBuilder {
	thiz.where = append(thiz.where, field+" "+operator+" "+thiz.value(value))
	return thiz
}

func (thiz *Builder) WhereClosure(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	thiz.where = append(thiz.where, builder.BuildWhereAnd())
	return thiz
}

func (thiz *Builder) WhereIn(field string, values ...interface{}) proxy.IQueryBuilder {
	thiz.where = append(thiz.where, field+" IN ("+thiz.values(values...)+")")
	return thiz
}

func (thiz *Builder) WhereAnd(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	builtWhere := builder.BuildWhere()
	if builtWhere != "" {
		thiz.where = append(thiz.where, "("+builder.BuildWhereAnd()+")")
	}
	return thiz
}

func (thiz *Builder) WhereOr(closure func(builder proxy.IQueryBuilder) proxy.IQueryBuilder) proxy.IQueryBuilder {
	builder := closure(NewBuilder(thiz.GetConnection()))
	builtWhere := builder.BuildWhereOr()
	if builtWhere != "" {
		thiz.where = append(thiz.where, "("+builder.BuildWhereOr()+")")
	}
	return thiz
}

func (thiz *Builder) OrderBy(field string, direction string) proxy.IQueryBuilder {
	thiz.order = append(thiz.order, field+" "+direction)
	return thiz
}

func (thiz *Builder) Limit(limit int) proxy.IQueryBuilder {
	thiz.limit = &limit
	return thiz
}

func (thiz *Builder) Offset(offset int) proxy.IQueryBuilder {
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

	res := thiz.GetDriver().Raw(thiz.ToQuery()).First(dest)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Count(dest *int64) error {
	thiz.Select("count(*)")
	res := thiz.GetDriver().Raw(thiz.ToQuery()).Scan(dest)
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

	res := thiz.GetDriver().Raw(thiz.ToQuery()).Find(dest)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Insert(model any) error {
	rvScan := reflect.ValueOf(model)
	if rvScan.Kind() != reflect.Ptr {
		return fmt.Errorf("Dest must be pointer")
	}

	res := thiz.GetDriver().Create(model)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Save(model any) error {
	res := thiz.GetDriver().Save(model)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Delete() error {
	res := thiz.GetDriver().Raw(thiz.DeleteQuery())
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) DeleteModel(model any) error {
	res := thiz.GetDriver().Delete(model)
	thiz.clearQueryParams()
	return res.Error
}

func (thiz *Builder) Relation(relation string) proxy.IQueryBuilder {
	if thiz.copyGorm == nil {
		panic("You must specify model. Call postgres.Builder.FromModel(model any)")
	}
	thiz.copyGorm = thiz.copyGorm.Preload(relation)
	return thiz
}

func (thiz *Builder) GetConnection() proxy.IConnection {
	return thiz.gormInstance
}

func (thiz *Builder) GetDriver() *gorm.DB {
	if thiz.copyGorm == nil {
		return thiz.GetConnection().Driver().(*gorm.DB)
	}
	return thiz.copyGorm
}

func (thiz *Builder) StartTransaction() proxy.IQueryBuilder {
	thiz.GetDriver().Begin()
	return thiz
}

func (thiz *Builder) EndTransaction() proxy.IQueryBuilder {
	thiz.GetDriver().Commit()
	thiz.clearQueryParams()
	return thiz
}

func (thiz *Builder) RollbackTransaction() proxy.IQueryBuilder {
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

func (thiz *Builder) value(value interface{}) string {
	switch value.(type) {
	case string:
		return "'" + value.(string) + "'"
	default:
		return fmt.Sprintf("%v", value)
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
		gormTag := nestedRelation.Field.Tag.Get("grgorm")
		splitGormTag := strings.Split(gormTag, ",")
		preload := false
		for _, value := range splitGormTag {
			if strings.Contains(value, "preload") {
				preload = true
			}
		}
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
