package cp

import (
	"github.com/osbits/gorgany/v2/model"
	"github.com/osbits/gorgany/v2/util"
	"reflect"
)

type FieldType string

const (
	Input           FieldType = "INPUT"
	TextArea        FieldType = "TEXTAREA"
	Enum            FieldType = "ENUM"
	Select          FieldType = "SELECT"
	Multiple        FieldType = "MULTIPLE"
	Date            FieldType = "DATE"
	DateTime        FieldType = "DATE_TIME"
	LocalizedString FieldType = "LOCALIZED_STRING"
	Checkbox        FieldType = "CHECKBOX"
	File            FieldType = "FILE"

	Domain               = "Domain"
	DomainMeta           = "DomainMeta"
	LocalizedStringModel = "LocalizedString"
)

type ValueWrapper struct {
	Value          any // it can be something like ID
	FormattedValue string
}

type PaginatedParams struct {
	Collection []*DomainParams
	Total      int
	Offset     int
	PerPage    int
}

func (thiz *PaginatedParams) DomainName() string {
	if len(thiz.Collection) == 0 {
		return ""
	}
	return thiz.Collection[0].DomainName
}

func (thiz *PaginatedParams) HeaderFields() []*FieldParams {
	if len(thiz.Collection) == 0 {
		return nil
	}
	return thiz.Collection[0].Fields
}

type DomainParams struct {
	Id         any
	DomainName string
	Fields     []*FieldParams
}

func BuildPaginatedParams[T any](paginatedCollection *model.PaginatedCollection[T]) (*PaginatedParams, error) {
	params := &PaginatedParams{
		Collection: make([]*DomainParams, 0),
		Total:      paginatedCollection.Total,
		Offset:     paginatedCollection.Offset,
		PerPage:    paginatedCollection.PerPage,
	}

	for _, domain := range paginatedCollection.Collection {
		domainParams, err := BuildParams(domain, true)
		if err != nil {
			return nil, err
		}

		params.Collection = append(params.Collection, domainParams)
	}

	// An empty page still has to describe its columns, so one zero value is built to
	// supply the header row.
	//
	// It must be built in index mode, like the loop above. Without the flag it was
	// built in *edit* mode, which is wrong twice over: the header honours
	// `grg-view:"edit=..."` instead of `list=...`, and building an edit field for a
	// relation loads that relation's rows to populate a select — so listing an empty
	// table fired one query per relation to fill a dropdown that is never rendered.
	// While db.Builder() returned nil that query was a nil dereference, which is why
	// the index page of an empty table answered 500 — the first page a freshly
	// migrated application shows.
	if len(paginatedCollection.Collection) == 0 {
		var emptyDomain T
		domainParams, err := BuildParams(emptyDomain, true)
		if err != nil {
			return nil, err
		}
		params.Collection = append(params.Collection, domainParams)
	}

	return params, nil
}

func BuildParams(domain any, isIndexAction ...bool) (*DomainParams, error) {
	isIndex := false
	if len(isIndexAction) > 0 {
		isIndex = isIndexAction[0]
	}

	fieldParams, err := buildFieldParams(domain, isIndex, nil)
	if err != nil {
		return nil, err
	}

	copyFieldParams := fieldParams

	if isIndex {
		fieldParams = util.FindAll(fieldParams, func(el *FieldParams) bool {
			return el.showInList == true
		})

		if len(fieldParams) == 0 {
			fieldParams = util.FindAll(copyFieldParams, func(el *FieldParams) bool {
				return !el.ignoreInList
			})
		}
	} else {
		fieldParams = util.FindAll(fieldParams, func(el *FieldParams) bool {
			return el.showInEdit == true
		})

		if len(fieldParams) == 0 {
			fieldParams = util.FindAll(copyFieldParams, func(el *FieldParams) bool {
				return !el.ignoreInEdit
			})
		}
	}

	rvDomain := util.IndirectValue(reflect.ValueOf(domain))
	rtDomain := rvDomain.Type()

	domainParams := &DomainParams{
		Id:         rvDomain.FieldByName("Id").Interface(), // todo
		DomainName: rtDomain.Name(),
		Fields:     fieldParams,
	}

	return domainParams, nil
}
