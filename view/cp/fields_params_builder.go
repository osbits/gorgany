package cp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/service/cache"
	"github.com/osbits/gorgany/v2/util"
	"gorm.io/gorm"
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

		// Skip anything that is neither a column nor a relation.
		//
		// A field the mapper was told to ignore — `gorm:"-"` — has no data type and no
		// relation. orm.EntityMeta is the one every generated domain carries, and it was
		// being rendered as a text input on every create and edit form: an unlabelled box
		// named "Meta" that posts ORM bookkeeping back as if it were user data. The name
		// check above catches it only when embedded anonymously, and the generator emits it
		// as a named field.
		if schemeField == nil || (schemeField.DataType == "" && relation == nil) {
			continue
		}

		scaffoldingParam, err := processDomainField(structField, field, structField.Name, schemeField.DataType, relation, isIndexAction)
		if err != nil {
			return nil, err
		}

		overriddenFields[structField.Name] = true
		fieldParams = append(fieldParams, scaffoldingParam)
	}

	// Descend into every embedded struct that survived the loop above, which has already
	// dropped the framework's own Domain and DomainMeta bases.
	//
	// This used to descend only into an embedded type satisfying core.IDomain[any] — Query,
	// Clone and GetDomainMeta. v2 deprecated Query on generated domains and the generator
	// stopped emitting any of the three, so no generated domain satisfies it any more, and
	// the standard shape the generator produces is an extension struct embedding exactly
	// such a domain:
	//
	//	type ProductCategory struct {
	//		generated.ProductCategory `grgorm:"generated"`
	//		...
	//	}
	//
	// Every real column lives on that embedded struct, so the gate silently reduced both the
	// list table and the edit form to whatever the extension declared itself — typically
	// nothing but relations. An embedded struct reached here carries columns by definition;
	// whether it also carries three ORM methods says nothing about that.
	for _, fieldName := range embeddedFields {
		field := rvDomain.FieldByName(fieldName)

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

// processFieldWithRelation loads the rows a relation field can be set to, so an edit form
// can render them as options.
//
// The read runs on its own short-lived session obtained from the db package's global
// context rather than through the container: this is reached by reflection from a view
// builder that is handed a schema and a value and nothing else, and threading a
// dependency down to it would mean changing BuildParams and BuildPaginatedParams, which
// every generated application calls.
//
// context.Background() is used for the same reason — no request context reaches here. That
// is tolerable because the query is a fixed, argument-free read of one lookup table, so
// there is no user input in the predicate and nothing to attribute to a caller. It does
// mean the read is not cancelled when the client goes away.
//
// Note this loads the whole table to populate one <select>. That is the pre-existing
// contract and callers depend on seeing every option, so it is left alone — but it makes
// an edit form on a domain related to a large table expensive, and a picker that pages or
// searches is the real answer.
func processFieldWithRelation(relation *schema.Relationship, fieldParams *FieldParams) ([]ValueWrapper, error) {
	var joinTableModel *schema.Schema

	if relation.JoinTable != nil {
		joinTableModel = relation.JoinTable
	} else {
		joinTableModel = relation.FieldSchema
	}

	primaryKeyField := joinTableModel.PrioritizedPrimaryField.StructField.Name

	dataSource := db.Connection()
	if dataSource == nil {
		return nil, fmt.Errorf("cp: no default database connection is registered, so the options for relation %q cannot be loaded", relation.Name)
	}

	session, err := dataSource.NewSession()
	if err != nil {
		return nil, fmt.Errorf("cp: opening a session to load options for relation %q: %w", relation.Name, err)
	}
	defer session.Close()

	// Scan wants a pointer to a concrete slice. The element type is *Model, matching what
	// GetSliceFromAny and the Stringer check below expect to iterate.
	slicePtr := reflect.New(reflect.SliceOf(reflect.PointerTo(joinTableModel.ModelType)))

	builder := session.Query().From(joinTableModel.Table)
	if result := session.Executor().Find(context.Background(), builder, slicePtr.Interface()); result.Error != nil {
		return nil, fmt.Errorf("cp: loading options for relation %q: %w", relation.Name, result.Error)
	}

	list := slicePtr.Elem().Interface()

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

// asTime coerces a timestamp-shaped value to a time.Time.
//
// A date field is not always a time.Time. gorm.DeletedAt is the one every soft-deleting
// domain has — softDelete generates a `Deleted gorm.DeletedAt` field whose gorm DataType
// is Time, so it arrives here looking like any other timestamp — and sql.NullTime,
// *time.Time and any driver.Valuer reach the same place. A bare value.(time.Time)
// assertion panicked on all of them, which took out every CP page of every domain that
// soft-deletes.
//
// ok is false when there is nothing to show, which covers a null wrapper and the zero
// instant alike. Both must render empty rather than as 0001-01-01: a nullable timestamp
// column scans into a non-pointer time.Time, so "never set" and "set to the zero instant"
// are indistinguishable, and printing a year-one date invites reading an unset field as
// data.
func asTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case time.Time:
		return v, !v.IsZero()
	case *time.Time:
		if v == nil {
			return time.Time{}, false
		}
		return *v, !v.IsZero()
	case gorm.DeletedAt:
		return v.Time, v.Valid && !v.Time.IsZero()
	case sql.NullTime:
		return v.Time, v.Valid && !v.Time.IsZero()
	}

	// Anything else able to hand over its underlying value — this is where the nullable
	// wrappers a host application defines for its own columns land.
	if valuer, ok := value.(driver.Valuer); ok {
		driverValue, err := valuer.Value()
		if err != nil || driverValue == nil {
			return time.Time{}, false
		}
		if t, ok := driverValue.(time.Time); ok {
			return t, !t.IsZero()
		}
	}
	return time.Time{}, false
}

func processFieldValue(value any, fieldType FieldType) (ValueWrapper, error) {
	if value == nil {
		return ValueWrapper{}, nil
	}
	switch fieldType {
	case Date:
		t, ok := asTime(value)
		if !ok {
			return ValueWrapper{}, nil
		}
		return ValueWrapper{
			Value:          nil,
			FormattedValue: t.Format("2006-01-02"),
		}, nil
	case DateTime:
		t, ok := asTime(value)
		if !ok {
			return ValueWrapper{}, nil
		}
		return ValueWrapper{
			Value:          nil,
			FormattedValue: t.Format("2006-01-02T15:04"),
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
