package plugin

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany"
	"gorgany/util"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"reflect"
	"strings"
)

const StructDefaultColumn = "model_struct"

type StructNamer interface {
	StructName() string
}

func StructName(model any) string {
	if castedModel, ok := model.(StructNamer); ok {
		return castedModel.StructName()
	}
	rType := reflect.TypeOf(model)
	return rType.Name()
}

func StructModelColumn() string {
	structModelColumn := viper.GetString("gorm.model.embed.structColumn")
	if structModelColumn == "" {
		return StructDefaultColumn
	}
	return structModelColumn
}

type ExtendedModelProcessor struct {
	namingStrategyService schema.NamingStrategy
}

func (thiz ExtendedModelProcessor) AddModelTypeAfterInsert(db *gorm.DB) {
	model := db.Statement.Model
	rValue := util.IndirectValue(db.Statement.ReflectValue)
	if rValue.Kind() == reflect.Slice {
		model = reflect.MakeSlice(rValue.Type(), 1, 1).Index(0).Interface()
	}

	rType := util.IndirectType(reflect.TypeOf(model))
	model = reflect.New(rType).Elem().Interface()

	abstractModels := thiz.abstractModels(model)
	if len(abstractModels) == 0 {
		return
	}

	for _, abstractModel := range abstractModels {
		rModelValue := reflect.ValueOf(abstractModel)
		tableName := thiz.namingStrategyService.TableName(rModelValue.Type().Name())
		primaryFields := db.Model(model).Statement.Schema.PrimaryFields

		conds := make([]string, 0)
		for _, primaryField := range primaryFields {
			field := rModelValue.FieldByName(primaryField.Name)
			conds = append(conds,
				fmt.Sprintf("%s = %s",
					thiz.namingStrategyService.ColumnName(tableName, primaryField.Name), thiz.value(field.Interface()),
				),
			)
		}

		rawDb, err := db.DB()
		if err != nil {
			panic(err)
		}
		query := fmt.Sprintf("UPDATE %s SET %s = '%s' WHERE %s", tableName, StructModelColumn(), StructName(model), strings.Join(conds, ","))
		_, err = rawDb.Exec(query)
		if err != nil {
			panic(err)
		}
	}
}

func (thiz ExtendedModelProcessor) AddModelTypeToWhere(db *gorm.DB) {
	model := db.Statement.Model

	rValue := util.IndirectValue(db.Statement.ReflectValue)
	if rValue.Kind() == reflect.Slice {
		model = reflect.MakeSlice(rValue.Type(), 1, 1).Index(0).Interface()
	}

	rType := util.IndirectType(reflect.TypeOf(model))
	model = reflect.New(rType).Elem().Interface()

	if !thiz.hasAbstractModel(model) {
		return
	}

	db.Where(fmt.Sprintf("%s = '%s'", StructModelColumn(), StructName(model)))
}

func (thiz ExtendedModelProcessor) hasAbstractModel(model any) bool {
	rModel := util.IndirectType(reflect.TypeOf(model))

	hasAbstractModel := false
	for i := 0; i < rModel.NumField(); i++ {
		field := rModel.Field(i)
		gorganyTag, ok := field.Tag.Lookup(gorgany.GorganyFieldTag)
		if field.Anonymous && field.Type.Kind() == reflect.Struct && ok {
			_, found := util.FindValueInTagValues(gorgany.ExtendsValue, gorganyTag, ",")
			if found {
				hasAbstractModel = true
			}
		}
	}
	return hasAbstractModel
}

func (thiz ExtendedModelProcessor) value(value interface{}) string {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return fmt.Sprintf("%v", value)
	default:
		val := fmt.Sprintf("%v", value)
		//escape single quotes: ' -> ''
		val = strings.ReplaceAll(val, "'", "''")
		return fmt.Sprintf("'%s'", val)
	}
}

func (thiz ExtendedModelProcessor) abstractModels(model any) []any {
	rType := util.IndirectType(reflect.TypeOf(model))

	abstractFieldNames := make([]string, 0)
	for i := 0; i < rType.NumField(); i++ {
		rField := rType.Field(i)

		gorganyTag, ok := rField.Tag.Lookup(gorgany.GorganyFieldTag)
		if rField.Anonymous && rField.Type.Kind() == reflect.Struct && ok {
			_, found := util.FindValueInTagValues(gorgany.ExtendsValue, gorganyTag, ",")
			if found {
				abstractFieldNames = append(abstractFieldNames, rField.Name)
			}
		}
	}

	abstractModels := make([]any, 0)
	rValue := util.IndirectValue(reflect.ValueOf(model))

	for _, fieldName := range abstractFieldNames {
		field := rValue.FieldByName(fieldName)
		abstractModels = append(abstractModels, field.Interface())
	}

	return abstractModels
}
