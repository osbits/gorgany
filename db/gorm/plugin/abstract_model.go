package plugin

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany/app/core"
	"gorgany/util"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"reflect"
	"strings"
	"unsafe"
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
	rValue := db.Statement.ReflectValue
	values := make([]any, 0)

	if rValue.Kind() == reflect.Slice {
		for i := 0; i < rValue.Len(); i++ {
			elem := rValue.Index(i)
			if elem.Kind() == reflect.Ptr {
				elem = elem.Elem()
			}

			elem = reflect.NewAt(elem.Type(), unsafe.Pointer(elem.UnsafeAddr()))
			values = append(values, elem.Interface().(any))
		}
	} else {
		rValue := reflect.NewAt(rValue.Type(), unsafe.Pointer(rValue.UnsafeAddr()))
		values = append(values, rValue.Interface().(any))
	}

	for _, value := range values {
		rType := util.IndirectType(reflect.TypeOf(value))
		model := reflect.New(rType).Elem().Interface()

		parentStruct := thiz.findParentStruct(rType)
		if parentStruct == nil {
			return
		}

		tableName := ""
		if tabler, ok := (value).(schema.Tabler); ok {
			tableName = (tabler).TableName()
		} else {
			tableName = thiz.namingStrategyService.TableName(rType.Name())
		}
		primaryFields := db.Model(model).Statement.Schema.PrimaryFields

		conds := make([]string, 0)
		for _, primaryField := range primaryFields {
			field := rValue.FieldByName(primaryField.Name)
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

	db.Where(fmt.Sprintf("%s = ?", StructModelColumn()), StructName(model))
}

func (thiz ExtendedModelProcessor) hasAbstractModel(model any) bool {
	rModel := util.IndirectType(reflect.TypeOf(model))

	if thiz.findParentStruct(rModel) == nil {
		return false
	}

	return true
}

func (thiz ExtendedModelProcessor) value(value interface{}) string {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		return fmt.Sprintf("'%v'", value)
	default:
		val := fmt.Sprintf("%v", value)
		//escape single quotes: ' -> ''
		val = strings.ReplaceAll(val, "'", "''")
		return fmt.Sprintf("'%s'", val)
	}
}

func (thiz ExtendedModelProcessor) findParentStruct(rModel reflect.Type) any {
	for i := 0; i < rModel.NumField(); i++ {
		rField := rModel.Field(i)

		gorganyTag, ok := rField.Tag.Lookup(core.GorganyFieldTag)
		if !rField.Anonymous && rField.Type.Kind() != reflect.Struct && !ok {
			continue
		}

		_, generatedDomainTag := util.FindValueInTagValues(core.GeneratedDomainTagValue, gorganyTag, ",")
		if generatedDomainTag {
			return thiz.findParentStruct(rField.Type)
		}

		_, found := util.FindValueInTagValues(core.ExtendsValue, gorganyTag, ",")
		if found {
			indirectRModel := util.IndirectType(rField.Type)
			rvModel := reflect.New(indirectRModel)
			return rvModel.Interface()
		}
	}

	return nil
}
