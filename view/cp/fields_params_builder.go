package cp

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm/schema"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FieldParamsList []*FieldParams

func (thiz FieldParamsList) Len() int {
	return len(thiz)
}

func (thiz FieldParamsList) Less(i, j int) bool {
	if thiz[i].Index < thiz[j].Index {
		return true
	}
	return false
}

func (thiz FieldParamsList) Swap(i, j int) {
	thiz[i], thiz[j] = thiz[j], thiz[i]
}

type FieldParams struct {
	ValueWrapper    ValueWrapper
	Name            string
	AvailableValues []ValueWrapper
	FieldType       FieldType
	Index           int

	ViewOnly bool

	showInList   bool
	ignoreInList bool

	showInEdit   bool
	ignoreInEdit bool
}

func buildFieldParams(domain any, isIndexAction bool, overriddenFields map[string]bool) ([]*FieldParams, error) {
	if overriddenFields == nil {
		overriddenFields = make(map[string]bool)
	}

	fieldParams := make(FieldParamsList, 0)

	domainScheme := cache.GetDomainSchemeCache().ParseDomain(domain)

	rvDomain := util.IndirectValue(reflect.ValueOf(domain))
	rtDomain := rvDomain.Type()

	embeddedFields := make([]string, 0)

	for i := 0; i < rvDomain.NumField(); i++ {
		structField := rtDomain.Field(i)
		field := rvDomain.Field(i)

		if _, ok := overriddenFields[structField.Name]; ok {
			continue
		}

		if structField.Anonymous {
			if structField.Name == Domain || structField.Name == DomainMeta {
				continue
			}
			embeddedFields = append(embeddedFields, structField.Name)
			continue
		}

		schemeField := domainScheme.FieldsByName[structField.Name]
		relation := domainScheme.Relationships.Relations[structField.Name]

		scaffoldingParam, err := processDomainField(structField, field, structField.Name, schemeField.DataType, relation, isIndexAction)
		if err != nil {
			return nil, err
		}

		overriddenFields[structField.Name] = true
		fieldParams = append(fieldParams, scaffoldingParam)
	}

	for _, fieldName := range embeddedFields {
		field := rvDomain.FieldByName(fieldName)
		if field.CanAddr() && !util.IsGenericImplemented(field.Addr().Interface(), (*core.IDomain[any])(nil)) { // todo: Check .CanAddr
			continue
		}

		nestedParams, err := buildFieldParams(field.Interface(), isIndexAction, overriddenFields)
		if err != nil {
			return nil, err
		}
		fieldParams = append(fieldParams, nestedParams...)
	}

	sort.Sort(fieldParams)

	for _, field := range fieldParams {
		relation := domainScheme.Relationships.Relations[field.Name]
		if relation == nil {
			continue
		}

		for _, reference := range relation.References {
			for _, f := range fieldParams {
				if f.Name == reference.ForeignKey.Name {
					f.ignoreInList = true
					f.ignoreInEdit = true
				}
			}
		}
	}

	return fieldParams, nil
}

func processDomainField(structField reflect.StructField, reflectedValue reflect.Value, fieldName string, schemeDataType schema.DataType, relation *schema.Relationship, isIndexAction bool) (*FieldParams, error) {
	rValue := util.IndirectValue(reflectedValue)

	tag := structField.Tag.Get(core.GrgViewTag)

	fieldParams := &FieldParams{Name: fieldName}

	index, found := util.FindValueInTagValues(string(core.GrgViewIndex), tag, ";")

	useDefaultIndex := true
	if found {
		keyValue := core.GrgViewTagKeyValuePair(index)
		value := keyValue.Value()
		if keyValue.Value() != "" {
			indexNumber, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			fieldParams.Index = indexNumber
			useDefaultIndex = false
		}
	}
	if useDefaultIndex {
		fieldParams.Index = structField.Index[0]
	}

	splitTagKeyValues := strings.Split(tag, ";")
	for _, keyValueRaw := range splitTagKeyValues {
		keyValue := core.GrgViewTagKeyValuePair(keyValueRaw)
		key := keyValue.Key()
		value := keyValue.Value()
		if key == core.GrgViewEdit && !isIndexAction {
			if value == string(core.GrgViewShow) {
				fieldParams.showInEdit = true
			} else if value == string(core.GrgViewIgnore) {
				fieldParams.ignoreInEdit = true
			} else if value == string(core.GrgViewViewOnly) {
				fieldParams.ViewOnly = true
			}
		} else if key == core.GrgViewList && isIndexAction {
			if value == string(core.GrgViewShow) {
				fieldParams.showInList = true
			} else if value == string(core.GrgViewIgnore) {
				fieldParams.ignoreInList = true
			}
		}
	}

	if relation != nil {
		if relation.FieldSchema.Name == LocalizedStringModel {
			fieldParams.FieldType = LocalizedString
		} else {
			if relation.Type == schema.HasMany || relation.Type == schema.Many2Many {
				fieldParams.FieldType = Multiple
			} else {
				fieldParams.FieldType = Select
			}

			if !isIndexAction {
				availableValues, err := processFieldWithRelation(relation, fieldParams)
				if err != nil {
					return nil, err
				}

				fieldParams.AvailableValues = availableValues
			}
		}
	} else {
		switch schemeDataType {
		case schema.Bool:
			fieldParams.FieldType = Checkbox
		case schema.Time:
			fieldParams.FieldType = resolveDateType(tag)
		default:
			if reflectedValue.Type().Implements(reflect.TypeOf((*core.IFile)(nil)).Elem()) {
				fieldParams.FieldType = File
			} else {
				fieldParams.FieldType = resolveDefaultType(tag, fieldParams)
			}
		}
	}

	if !rValue.IsValid() {
		return fieldParams, nil
	}

	val := reflectedValue.Interface()

	if nullableValue, ok := val.(core.NullableValueGetter); ok {
		val = nullableValue.GetValue()
	}

	if nullableValue, ok := val.(core.IFormValue); ok {
		var err error
		val, err = nullableValue.Value()
		if err != nil {
			return nil, err
		}
	}

	valWrapper, err := processFieldValue(val, fieldParams.FieldType)
	if err != nil {
		return nil, err
	}
	fieldParams.ValueWrapper = valWrapper
	return fieldParams, nil
}

func processFieldWithRelation(relation *schema.Relationship, fieldParams *FieldParams) ([]ValueWrapper, error) {
	builder := db.Builder()

	var list any

	var joinTableModel *schema.Schema

	if relation.JoinTable != nil {
		joinTableModel = relation.JoinTable
	} else {
		joinTableModel = relation.FieldSchema
	}

	primaryKeyField := joinTableModel.PrioritizedPrimaryField.StructField.Name

	sliceType := reflect.SliceOf(reflect.New(joinTableModel.ModelType).Type())
	rList := reflect.MakeSlice(sliceType, 0, 0)
	list = rList.Interface()

	builder.From(joinTableModel.Table)

	err := builder.List(&list)
	if err != nil {
		return nil, err
	}

	availableValues := make([]ValueWrapper, 0)
	anySlice := util.GetSliceFromAny(list)
	for _, el := range anySlice {
		primaryField := util.IndirectValue(reflect.ValueOf(el)).FieldByName(primaryKeyField)
		primaryKeyValue := primaryField.Interface()

		name := fmt.Sprintf("%s [%v]", joinTableModel.Name, primaryKeyValue)
		if stringer, ok := el.(fmt.Stringer); ok {
			name = stringer.String()
		}

		availableValues = append(availableValues, ValueWrapper{
			Value:          primaryKeyValue,
			FormattedValue: name,
		})
	}
	return availableValues, nil
}

func resolveDefaultType(tag string, fieldParams *FieldParams) FieldType {
	defaultType := Input
	fieldType, found := util.FindValueInTagValues(string(core.GrgViewType), tag, ";")
	if found {
		keyValue := core.GrgViewTagKeyValuePair(fieldType)
		if keyValue.Value() == string(core.GrgViewDateTextarea) {
			defaultType = TextArea
		}
	} else {
		enum, found := util.FindValueInTagValues(string(core.GrgViewEnum), tag, ";")
		if found {
			keyValue := core.GrgViewTagKeyValuePair(enum)
			tagValue := keyValue.Value()
			enumValues := strings.Split(tagValue, ",")
			defaultType = Enum
			fieldParams.AvailableValues = make([]ValueWrapper, 0)
			for _, enumValue := range enumValues {
				fieldParams.AvailableValues = append(fieldParams.AvailableValues, ValueWrapper{
					Value:          enumValue,
					FormattedValue: enumValue,
				})
			}
		}
	}
	return defaultType
}

func resolveDateType(tag string) FieldType {
	defaultTimeType := DateTime

	fieldType, found := util.FindValueInTagValues(string(core.GrgViewType), tag, ";")
	if found {
		keyValue := core.GrgViewTagKeyValuePair(fieldType)
		tagValue := keyValue.Value()
		if tagValue == string(core.GrgViewDate) {
			defaultTimeType = Date
		} else if tagValue == string(core.GrgViewDateTime) {
			defaultTimeType = DateTime
		}
	}
	return defaultTimeType
}

func processFieldValue(value any, fieldType FieldType) (ValueWrapper, error) {
	if value == nil {
		return ValueWrapper{}, nil
	}
	switch fieldType {
	case Date:
		return ValueWrapper{
			Value:          nil,
			FormattedValue: value.(time.Time).Format("2006-01-02"),
		}, nil
	case DateTime:
		return ValueWrapper{
			Value:          nil,
			FormattedValue: value.(time.Time).Format("2006-01-02T15:04"),
		}, nil
	case Select:
		rvValue := util.IndirectValue(reflect.ValueOf(value))
		rtValue := rvValue.Type()
		domainScheme := cache.GetDomainSchemeCache().ParseDomain(value)

		primaryKey := domainScheme.PrioritizedPrimaryField
		primaryField := rvValue.FieldByName(primaryKey.StructField.Name)

		primaryKeyValue := primaryField.Interface()

		name := fmt.Sprintf("%s [%v]", rtValue.Name(), primaryKeyValue)
		if stringer, ok := value.(fmt.Stringer); ok {
			name = stringer.String()
		}

		return ValueWrapper{
			Value:          primaryKeyValue,
			FormattedValue: name,
		}, nil
	case Multiple:
		slice := util.GetSliceFromAny(value)
		name := make([]string, 0)
		ids := make([]any, 0)
		for _, el := range slice {
			rvValue := util.IndirectValue(reflect.ValueOf(el))
			rtValue := rvValue.Type()
			domainScheme := cache.GetDomainSchemeCache().ParseDomain(value)

			primaryKey := domainScheme.PrioritizedPrimaryField
			primaryField := rvValue.FieldByName(primaryKey.StructField.Name)
			primaryKeyValue := primaryField.Interface()

			elName := fmt.Sprintf("%s [%v]", rtValue.Name(), primaryKeyValue)

			if stringer, ok := el.(fmt.Stringer); ok {
				elName = stringer.String()
			}
			ids = append(ids, primaryKeyValue)
			name = append(name, elName)
		}

		return ValueWrapper{
			Value:          ids,
			FormattedValue: strings.Join(name, ", "),
		}, nil
	case File:
		if value == nil {
			return ValueWrapper{
				Value: nil,
			}, nil
		}
		file := value.(core.IFile)
		return ValueWrapper{
			Value:          file.PublicPath(),
			FormattedValue: file.GetName(),
		}, nil
	default:
		rvValue := util.IndirectValue(reflect.ValueOf(value))
		rawValue := rvValue.Interface()
		return ValueWrapper{
			Value:          rawValue,
			FormattedValue: fmt.Sprintf("%v", rawValue),
		}, nil
	}
}
