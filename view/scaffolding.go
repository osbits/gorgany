package view

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm/schema"
	"reflect"
	"strings"
	"time"
)

type ScaffoldingFieldType string

const (
	Input           ScaffoldingFieldType = "INPUT"
	TextArea        ScaffoldingFieldType = "TEXTAREA"
	Select          ScaffoldingFieldType = "SELECT"
	Multiple        ScaffoldingFieldType = "MULTIPLE"
	Date            ScaffoldingFieldType = "DATE"
	DateTime        ScaffoldingFieldType = "DATE_TIME"
	LocalizedString ScaffoldingFieldType = "LOCALIZED_STRING"
	Checkbox        ScaffoldingFieldType = "CHECKBOX"

	Domain               = "Domain"
	DomainMeta           = "DomainMeta"
	LocalizedStringModel = "LocalizedString"
)

type ValueWrapper struct {
	Value          any // it can be something like ID
	FormattedValue string
}

type ScaffoldingPaginatedParams struct {
	Collection []*ScaffoldingDomainParams
	Total      int
	Offset     int
	PerPage    int
}

func (thiz *ScaffoldingPaginatedParams) DomainName() string {
	if len(thiz.Collection) == 0 {
		return ""
	}
	return thiz.Collection[0].DomainName
}

func (thiz *ScaffoldingPaginatedParams) HeaderFields() []*ScaffoldingFieldParams {
	if len(thiz.Collection) == 0 {
		return nil
	}
	return thiz.Collection[0].Fields
}

type ScaffoldingDomainParams struct {
	Id         any
	DomainName string
	Fields     []*ScaffoldingFieldParams
}

type ScaffoldingFieldParams struct {
	ValueWrapper    ValueWrapper
	Name            string
	AvailableValues []ValueWrapper
	FieldType       ScaffoldingFieldType
}

func BuildScaffoldingPaginatedParams[T any](paginatedCollection *model.PaginatedCollection[T]) (*ScaffoldingPaginatedParams, error) {
	params := &ScaffoldingPaginatedParams{
		Collection: make([]*ScaffoldingDomainParams, 0),
		Total:      paginatedCollection.Total,
		Offset:     paginatedCollection.Offset,
		PerPage:    paginatedCollection.PerPage,
	}

	for _, domain := range paginatedCollection.Collection {
		domainParams, err := BuildScaffoldingParams(domain)
		if err != nil {
			return nil, err
		}

		params.Collection = append(params.Collection, domainParams)
	}

	if len(paginatedCollection.Collection) == 0 {
		var emptyDomain T
		domainParams, err := BuildScaffoldingParams(emptyDomain)
		if err != nil {
			return nil, err
		}
		params.Collection = append(params.Collection, domainParams)
	}

	return params, nil
}

func BuildScaffoldingParams(domain any) (*ScaffoldingDomainParams, error) {
	params, err := buildScaffoldingFieldParams(domain, nil)
	if err != nil {
		return nil, err
	}

	rvDomain := util.IndirectValue(reflect.ValueOf(domain))
	rtDomain := rvDomain.Type()

	domainParams := &ScaffoldingDomainParams{
		Id:         rvDomain.FieldByName("Id").Interface(), // todo
		DomainName: rtDomain.Name(),
		Fields:     params,
	}

	return domainParams, nil
}

func buildScaffoldingFieldParams(domain any, overriddenFields map[string]bool) ([]*ScaffoldingFieldParams, error) {
	if overriddenFields == nil {
		overriddenFields = make(map[string]bool)
	}

	fieldParams := make([]*ScaffoldingFieldParams, 0)

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

		scaffoldingParam, err := processDomainField(structField, field, structField.Name, schemeField.DataType, relation)
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

		nestedParams, err := buildScaffoldingFieldParams(field.Interface(), overriddenFields)
		if err != nil {
			return nil, err
		}
		fieldParams = append(fieldParams, nestedParams...)
	}

	return fieldParams, nil
}

func processDomainField(structField reflect.StructField, reflectedValue reflect.Value, fieldName string, schemeDataType schema.DataType, relation *schema.Relationship) (*ScaffoldingFieldParams, error) {
	rValue := util.IndirectValue(reflectedValue)

	scaffoldingParam := &ScaffoldingFieldParams{Name: fieldName}

	if relation != nil {
		if relation.FieldSchema.Name == LocalizedStringModel {
			scaffoldingParam.FieldType = LocalizedString
		} else {
			if relation.Type == schema.HasMany || relation.Type == schema.Many2Many {
				scaffoldingParam.FieldType = Multiple
			} else {
				scaffoldingParam.FieldType = Select
			}
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

			scaffoldingParam.AvailableValues = availableValues
		}
	} else {
		switch schemeDataType {
		case schema.Bool:
			scaffoldingParam.FieldType = Checkbox
		case schema.Time:
			scaffoldingParam.FieldType = DateTime // todo: Currently allowed only DateTime, need to do just Date
		default:
			scaffoldingParam.FieldType = Input
		}
	}

	if !rValue.IsValid() {
		return scaffoldingParam, nil
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

	valWrapper, err := processFieldValue(val, scaffoldingParam.FieldType)
	if err != nil {
		return nil, err
	}
	scaffoldingParam.ValueWrapper = valWrapper
	return scaffoldingParam, nil
}

func processFieldValue(value any, fieldType ScaffoldingFieldType) (ValueWrapper, error) {
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
			rvValue := util.IndirectValue(reflect.ValueOf(value))
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
	default:
		return ValueWrapper{
			Value:          value,
			FormattedValue: fmt.Sprintf("%v", value),
		}, nil
	}
}
