package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/log"
	"github.com/osbits/gorgany/util"
)

type ApiReturnObject struct {
	Errors     []any
	HttpStatus core.HttpStatus
	Body       any
}

func (thiz *ApiReturnObject) MarshalJSON() ([]byte, error) {
	tmpStruct := struct {
		Status int    `json:"status"`
		Code   string `json:"status_code"`
		Body   any    `json:"body"`
		Errors []any  `json:"errors"`
	}{}

	tmpStruct.Status = thiz.HttpStatus.Status
	tmpStruct.Code = thiz.HttpStatus.Code

	var body any
	var e error

	if thiz.Body != nil {
		rvBody := util.IndirectValue(reflect.ValueOf(thiz.Body))
		body, e = thiz.callBuilderFunc(rvBody)
	}

	if e != nil {
		return nil, e
	}

	tmpStruct.Body = body

	tmpStruct.Errors = thiz.Errors

	return json.Marshal(tmpStruct)
}

func (thiz *ApiReturnObject) AddValidationError(e err.ValidationError) {
	if thiz.HttpStatus != core.ValidationHttpStatus {
		thiz.HttpStatus = core.ValidationHttpStatus
	}
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) AddError(e any, status core.HttpStatus) {
	thiz.HttpStatus = core.ValidationHttpStatus
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) SetHttpStatus(status core.HttpStatus) {
	thiz.HttpStatus = status
}

func (thiz *ApiReturnObject) SetBody(body any) {
	thiz.Body = body
}

func (thiz *ApiReturnObject) HasErrors() bool {
	return len(thiz.Errors) > 0
}

func (thiz *ApiReturnObject) callBuilderFunc(reflectedValue reflect.Value) (any, error) {
	var body any
	var e error

	if !reflectedValue.IsValid() {
		log.Log().Warnf("The value passed to the ApiReturnObject is invalid")
		return nil, nil
	}

	if reflectedValue.Kind() == reflect.Slice {
		body, e = thiz.buildBodySlice(reflectedValue)
	} else if reflectedValue.Kind() == reflect.Struct {
		body, e = thiz.buildBodyElement(reflectedValue)
	} else if reflectedValue.Kind() == reflect.Map {
		body, e = thiz.buildBodyMap(reflectedValue)
	} else {
		body = reflectedValue.Interface()
	}

	return body, e
}

func (thiz *ApiReturnObject) buildBodySlice(reflectSlice reflect.Value) ([]any, error) {
	slice := make([]any, 0)
	for i := 0; i < reflectSlice.Len(); i++ {

		var sliceElement any
		var e error

		rElement := util.IndirectValue(reflectSlice.Index(i))
		sliceElement, e = thiz.callBuilderFunc(rElement)

		if e != nil {
			return nil, e
		}

		slice = append(slice, sliceElement)
	}
	return slice, nil
}

func (thiz *ApiReturnObject) buildBodyMap(reflectMap reflect.Value) (map[string]any, error) {
	finalMap := make(map[string]any)
	for _, key := range reflectMap.MapKeys() {
		if key.Kind() != reflect.String {
			continue
		}

		value := reflectMap.MapIndex(key)

		rElement := util.IndirectValue(value)
		mapElement, e := thiz.callBuilderFunc(rElement)

		if e != nil {
			return nil, e
		}

		finalMap[key.String()] = mapElement
	}
	return finalMap, nil
}

func (thiz *ApiReturnObject) buildBodyElement(element reflect.Value) (any, error) {
	body := make(map[string]any)

	if marshaller, ok := element.Interface().(json.Marshaler); ok {
		fieldContent, e := marshaller.MarshalJSON()
		if e != nil {
			return nil, e
		}
		return json.RawMessage(fieldContent), nil
	}

	allowedFields := []string{"*"}
	if limitedFields, ok := element.Interface().(core.LimitedFieldsMarshaller); ok {
		allowedFields = limitedFields.AllowedFields()
	}

	if len(allowedFields) == 0 {
		return body, nil
	}

	for i := 0; i < element.NumField(); i++ {
		rvField := element.Field(i)
		rtField := element.Type().Field(i)

		// Unexported fields are never serialised.
		//
		// This includes an *embedded field of unexported type*, whose field name is
		// the lowercase type name. encoding/json does promote the exported fields of
		// such a struct; this marshaller cannot, because it reads values through
		// reflect.Value.Interface(), which panics on any value reached through an
		// unexported field. Skipping is what the pre-v2 code did too, so this is a
		// documented boundary rather than a change: embed an exported type to have
		// its fields inlined. See docs/DIALECTS.md's sibling note in MIGRATION_v2.md.
		if !rtField.IsExported() {
			continue
		}

		tag := parseJSONTag(rtField)

		// `json:"-"` means "never serialise this field". The envelope marshaller
		// does not go through encoding/json — it reflects field by field — and it
		// used to ignore the tag entirely and emit the field under its Go name. A
		// DTO field marked json:"-" to keep a password hash off the wire was
		// therefore serialised anyway. This is the security half of the fix.
		if tag.Skip {
			continue
		}

		if util.IndirectType(rvField.Type()).Kind() == reflect.Struct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				if rtField.Anonymous {
					nestedElement, e := thiz.buildBodyElement(util.IndirectValue(rvField))
					if e != nil {
						return nil, e
					}
					body = util.MergeMaps(body, nestedElement.(map[string]any))
				}
				continue
			}
		}

		// An anonymous embedded struct is inlined the way encoding/json inlines
		// it, so its fields appear at this level. Previously only an embedded
		// struct that happened to implement core.LimitedFieldsMarshaller was
		// inlined; every other one was nested under its Go *type* name, producing
		// {"OwnerCardDto": {...}} where encoding/json produces the fields inline.
		//
		// An embedded field with an explicit name in its json tag is NOT inlined,
		// matching encoding/json: the tag names it, so it becomes a real key.
		if rtField.Anonymous && !tag.HasName {
			if inlined, ok := thiz.inlineEmbedded(rvField); ok {
				nested, e := inlined()
				if e != nil {
					return nil, e
				}
				body = util.MergeMaps(body, nested)
				continue
			}
		}

		if !util.InArrayFunc(allowedFields, func(el string) bool {
			return strings.EqualFold(el, rtField.Name)
		}) && (allowedFields[0] != "*") {
			continue
		}

		// `omitempty` drops a zero value, as encoding/json does. It used to be
		// ignored, so an empty field shipped as null / "" / 0.
		if tag.OmitEmpty && rvField.IsZero() {
			continue
		}

		if nestedValue, e := thiz.callBuilderFunc(rvField); e == nil {
			body[tag.Name] = nestedValue
		} else {
			return nil, e
		}
	}

	return body, nil
}

// inlineEmbedded returns a builder for an anonymous embedded struct's fields when
// that struct should be inlined, matching encoding/json's rules: a struct (or a
// non-nil pointer to one) is inlined; anything else — an embedded interface, or an
// embedded named non-struct type — is a normal field.
func (thiz *ApiReturnObject) inlineEmbedded(rvField reflect.Value) (func() (map[string]any, error), bool) {
	if util.IndirectType(rvField.Type()).Kind() != reflect.Struct {
		return nil, false
	}

	// A nil embedded pointer contributes nothing, exactly as encoding/json emits
	// nothing for it.
	if rvField.Kind() == reflect.Ptr && rvField.IsNil() {
		return func() (map[string]any, error) { return map[string]any{}, nil }, true
	}

	value := util.IndirectValue(rvField)

	// A type with its own MarshalJSON is not inlined: encoding/json calls the
	// method and uses the whole result.
	if _, ok := value.Interface().(json.Marshaler); ok {
		return nil, false
	}
	if rvField.CanAddr() {
		if _, ok := rvField.Addr().Interface().(json.Marshaler); ok {
			return nil, false
		}
	}

	return func() (map[string]any, error) {
		nested, e := thiz.buildBodyElement(value)
		if e != nil {
			return nil, e
		}
		asMap, ok := nested.(map[string]any)
		if !ok {
			// buildBodyElement returned a json.RawMessage, meaning the embedded
			// type marshals itself. Nothing to inline.
			return nil, fmt.Errorf("cannot inline embedded field of type %s", value.Type())
		}
		return asMap, nil
	}, true
}

// jsonTag is a parsed `json:"..."` struct tag.
type jsonTag struct {
	// Name is the key to serialise under.
	Name string
	// HasName reports whether the tag named the field explicitly.
	HasName bool
	// Skip is true for `json:"-"`, meaning never serialise this field.
	Skip bool
	// OmitEmpty is true when the tag carries the omitempty option.
	OmitEmpty bool
}

// parseJSONTag parses a `json:"..."` tag with encoding/json's semantics.
//
// It used to return only a name, and treated `json:"-"` identically to an absent
// tag by returning the Go field name for both — which is how a field explicitly
// marked as never-serialise ended up on the wire.
//
// Note the one subtlety encoding/json also has: `json:"-"` means skip, while
// `json:"-,"` means "use the literal name -".
func parseJSONTag(rtField reflect.StructField) jsonTag {
	raw, ok := rtField.Tag.Lookup("json")
	if !ok || raw == "" {
		return jsonTag{Name: rtField.Name}
	}

	if raw == "-" {
		return jsonTag{Name: rtField.Name, Skip: true}
	}

	name, opts, _ := strings.Cut(raw, ",")

	tag := jsonTag{Name: rtField.Name}
	if name != "" {
		tag.Name = name
		tag.HasName = true
	}

	for _, opt := range strings.Split(opts, ",") {
		if opt == "omitempty" {
			tag.OmitEmpty = true
		}
	}

	return tag
}
