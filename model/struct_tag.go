package model

import (
	"reflect"
	"strings"
)

// The wire-name tags a DTO can carry. Order matters: `json` is what a JSON body binds
// through, `scheme` is what the query-string and multipart parsers bind through
// (http/json_parser.go's getTypeInfo is called with "scheme" from both). A DTO that
// only ever arrives as a query string carries only `scheme`, and vice versa, so both
// are consulted.
const (
	JSONTagName   = "json"
	SchemeTagName = "scheme"
)

// StructTag is a parsed encoding/json-style struct tag: a name, then comma-separated
// options.
type StructTag struct {
	// Name is the key to serialise under, or the Go field name if the tag named none.
	Name string
	// HasName reports whether the tag named the field explicitly.
	HasName bool
	// Skip is true for `-`, meaning never serialise this field.
	Skip bool
	// OmitEmpty is true when the tag carries the omitempty option.
	OmitEmpty bool
}

// ParseStructTag parses `<tagName>:"..."` with encoding/json's semantics.
//
// The json variant of this used to return only a name and treated `json:"-"`
// identically to an absent tag by returning the Go field name for both — which is how
// a field explicitly marked as never-serialise ended up on the wire (T3.5).
//
// Note the one subtlety encoding/json also has: `-` means skip, while `-,` means "use
// the literal name -".
func ParseStructTag(rtField reflect.StructField, tagName string) StructTag {
	raw, ok := rtField.Tag.Lookup(tagName)
	if !ok || raw == "" {
		return StructTag{Name: rtField.Name}
	}

	if raw == "-" {
		return StructTag{Name: rtField.Name, Skip: true}
	}

	name, opts, _ := strings.Cut(raw, ",")

	tag := StructTag{Name: rtField.Name}
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

// WireFieldName returns the name a client used for this field: its `json` tag, else its
// `scheme` tag, else the Go field name.
//
// This is what validation errors report, so a client can map an error back to the form
// field it sent. Validation errors used to carry the Go name (`MobilePhone` for a field
// the client sent as `mobile_phone`), which no client could match up.
//
// A field tagged `-` on the wire has no wire name, so the Go name is the only honest
// answer. That case is reachable: a `json:"-"` field can still carry validate rules,
// and something has to be reported when one of them fails.
func WireFieldName(rtField reflect.StructField) string {
	for _, tagName := range []string{JSONTagName, SchemeTagName} {
		if tag := ParseStructTag(rtField, tagName); tag.HasName {
			return tag.Name
		}
	}
	return rtField.Name
}
