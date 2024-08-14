package view

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm/schema"
	"reflect"
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
)

type ScaffoldingParam struct {
	Value any
	Name  string
	//Relations []any todo
	FieldType ScaffoldingFieldType
}

func BuildScaffoldingParams(domain any, extraParams map[string]any) ([]*ScaffoldingParam, error) {
	params := make([]*ScaffoldingParam, 0)

	domainScheme := cache.GetDomainSchemeCache().ParseDomain(domain)

	rvDomain := util.IndirectValue(reflect.ValueOf(domain))
	rtDomain := rvDomain.Type()

	embeddedFields := make([]string, 0)

	for i := 0; i < rvDomain.NumField(); i++ {
		structField := rtDomain.Field(i)
		field := rvDomain.Field(i)

		if structField.Anonymous {
			embeddedFields = append(embeddedFields, structField.Name)
			continue
		}

		scaffoldingParam, err := processModelValue(field, structField.Name, domainScheme)
		if err != nil {
			return nil, err
		}

		params = append(params, scaffoldingParam)

		// domain, NullableValue, time.Time, implementation of core.IFormValue, pointer, bool, string/numeric value
	}

	for _, fieldName := range embeddedFields {
		field := rvDomain.FieldByName(fieldName)
		if field.CanAddr() && !util.IsGenericImplemented(field.Addr().Interface(), (*core.IDomain[any])(nil)) { // todo: Check .CanAddr
			continue
		}

		nestedParams, err := BuildScaffoldingParams(field.Interface(), nil)
		if err != nil {
			return nil, err
		}
		params = append(params, nestedParams...)
	}

	return params, nil
	//return util.MergeMaps(params, extraParams)
}

func processModelValue(rValue reflect.Value, key string, scheme *schema.Schema) (*ScaffoldingParam, error) {
	rValue = util.IndirectValue(rValue)

	schemeField := scheme.FieldsByName[key]
	relation := scheme.Relationships.Relations[key]

	scaffoldingParam := &ScaffoldingParam{Name: key, Value: nil}

	if relation != nil {
		if relation.Type == schema.HasMany || relation.Type == schema.Many2Many {
			scaffoldingParam.FieldType = Multiple
		} else {
			scaffoldingParam.FieldType = Select
		}
	} else {
		switch schemeField.DataType {
		case schema.Bool:
			scaffoldingParam.FieldType = Checkbox
		case schema.Time:
			scaffoldingParam.FieldType = DateTime // todo: Read tag and make a decision which type we should use Date or DateTime
		default:
			scaffoldingParam.FieldType = Input
		}
	}

	if !rValue.IsValid() {
		return scaffoldingParam, nil
	}

	val := rValue.Interface()

	if util.IsGenericImplemented(val, (*core.IDomain[any])(nil)) {
		primaryKey := cache.GetDomainSchemeCache().ParseDomain(val).PrimaryFields[0]
		primaryKeyValue := rValue.FieldByName(primaryKey.StructField.Name)
		scaffoldingParam.Value = primaryKeyValue
		return scaffoldingParam, nil
	}

	if nullableValue, ok := val.(core.NullableValueGetter); ok { // todo: Need to process got rValue again, because it can be instance of IFormValue, where we should call interface method
		scaffoldingParam.Value = nullableValue.GetValue()
		return scaffoldingParam, nil
	}

	if nullableValue, ok := val.(core.IFormValue); ok {
		v, err := nullableValue.Value()
		if err != nil {
			return nil, err
		}
		scaffoldingParam.Value = v
		return scaffoldingParam, nil
	}

	if scaffoldingParam.FieldType == DateTime {
		scaffoldingParam.Value = rValue.Interface().(time.Time).Format("2006-01-02T15:04")
		return scaffoldingParam, nil
	}

	scaffoldingParam.Value = rValue.Interface()
	return scaffoldingParam, nil
}
