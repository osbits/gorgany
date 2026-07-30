package validator

import (
	"fmt"
	"reflect"

	goValidator "github.com/go-playground/validator/v10"
	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/i18n"
	"github.com/osbits/gorgany/v2/model"
	"github.com/osbits/gorgany/v2/util"
)

func New() core.IValidator {
	v := goValidator.New()

	// Report a field by the name the client used for it, not by its Go name.
	//
	// This is go-playground's own hook for the job, so it applies uniformly to
	// FieldError.Field() and FieldError.Namespace() and needs no second tag parser —
	// model.WireFieldName is the same tag handling the response marshaller uses.
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		return model.WireFieldName(field)
	})

	v.RegisterCustomTypeFunc(validateFile, model.File{})
	err := v.RegisterValidation("mime", validateMimeType, true)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("maxSize", validateFileSize)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("unique", validateUnique)
	if err != nil {
		panic(err)
	}

	v.RegisterCustomTypeFunc(validateLocalizedString, model.LocalizedString{})
	err = v.RegisterValidation("lsCompletelyRequired", validateRequiredLocalizedString) //all langs in LocalizedString must not be empty
	if err != nil {
		panic(err)
	}

	v.RegisterCustomTypeFunc(validateMapStringString, map[string]string{})
	err = v.RegisterValidation("mapStringStringCompletelyRequired", validateRequiredMapStringString) //all langs in LocalizedString must not be empty
	if err != nil {
		panic(err)
	}

	return &Validator{
		Validate: v,
	}
}

type Validator struct {
	*goValidator.Validate
}

var (
	_ core.IValidator          = (*Validator)(nil)
	_ core.ILocalizedValidator = (*Validator)(nil)
)

// ValidateStruct validates s and reports failures in the default locale.
//
// Use ValidateStructForLocale to render the messages in a request's locale; the HTTP
// input resolver does that automatically.
func (v *Validator) ValidateStruct(s any) error {
	return v.ValidateStructForLocale(s, i18n.DefaultLocale())
}

// ValidateStructForLocale validates s and renders its messages in locale.
func (v *Validator) ValidateStructForLocale(s any, locale string) error {
	overridden, err := v.overriddenFields(s)
	if err != nil {
		return err
	}

	err = v.StructExcept(s, overridden...)
	if err == nil {
		return nil
	}

	if _, ok := err.(*goValidator.InvalidValidationError); ok {
		return err
	}

	fieldErrors, ok := err.(goValidator.ValidationErrors)
	if !ok {
		// StructExcept documents these two as the only error types it produces. If a
		// future version adds a third, surface it rather than asserting it away.
		return err
	}

	validationErrors := make(error2.ValidationErrors, 0, len(fieldErrors))
	for _, e := range fieldErrors {
		// e.Field() is the wire name because New() registered a tag-name function.
		field := e.Field()
		validationErrors.AddValidationError(error2.ValidationError{
			Field: field,
			Err:   message(e, field, locale),
			Rule:  e.Tag(),
			Param: e.Param(),
			Path:  wireNamespace(e),
		})
	}

	if len(validationErrors) > 0 {
		return &validationErrors
	}

	return nil
}

// overriddenFields lists the namespaces to exclude from validation because a field of
// the outer struct shadows a field promoted from an embedded one. Validating both would
// report the same field twice, the second time against rules the client has no way to
// satisfy through the promoted path — the promoted field is unreachable in Go.
//
// The namespaces are built from **Go** field names, because that is what StructExcept
// matches against; the tag-name function rewrites what FieldError reports, not what
// StructExcept accepts. Verified, because getting this wrong is silent: an exclusion
// naming a namespace that does not exist is a no-op.
//
// The previous implementation had five faults, every one of them silent:
//
//  1. parentKey was reassigned *inside* the loop over embedded fields and accumulated
//     across iterations, so a struct embedding A and B yielded ["A.Name", "A.B.Name"]
//     where the second entry should have been "B.Name". Map iteration order is
//     randomised, so which struct got the corrupt namespace varied per run.
//  2. It keyed embedded fields by reflect.Type.Name(), which is "" for an embedded
//     pointer — collapsing every embedded pointer onto one map entry and dropping all
//     but one of them.
//  3. It called field.Addr(), which panics when the struct arrived by value.
//     ValidateStruct(SomeDto{}) panicked outright.
//  4. It recursed through an embedded pointer's *value*, so a nil embedded pointer
//     panicked with "reflect: call of reflect.Value.Type on zero Value".
//  5. It walked unexported fields, which validate rules cannot apply to anyway, and
//     which reflect cannot read.
//
// It now walks types rather than values, which removes faults 3 and 4 by construction,
// and derives the namespace per branch rather than mutating one in place.
//
// It also reports the collision the brief asked to be made loud: two distinct fields of
// the *same* struct sharing one wire name. That is not shadowing — it is a DTO the body
// parser cannot bind unambiguously, so one of the two silently stays zero and validation
// reports a name the client cannot match to either.
func (v *Validator) overriddenFields(s any) ([]string, error) {
	// The nil check has to come before IndirectType, which dereferences the type it is
	// given: ValidateStruct(nil) would panic inside it rather than reaching
	// StructExcept's InvalidValidationError.
	declared := reflect.TypeOf(s)
	if declared == nil {
		return nil, nil
	}

	rt := util.IndirectType(declared)
	if rt == nil || rt.Kind() != reflect.Struct {
		// Not a struct. StructExcept reports that far more precisely than this walk
		// can, so leave it to do so.
		return nil, nil
	}

	w := &overrideWalk{
		takenGoNames: map[string]bool{},
		seenTypes:    map[reflect.Type]bool{},
		excluded:     make([]string, 0),
	}
	if err := w.walk(rt, ""); err != nil {
		return nil, err
	}
	return w.excluded, nil
}

// overrideWalk carries the state of one overriddenFields walk.
type overrideWalk struct {
	// takenGoNames spans the whole walk, outer struct first, so an embedded field is
	// excluded when an outer one already claimed its Go name. This is the original
	// semantics, preserved deliberately.
	takenGoNames map[string]bool
	// seenTypes breaks the cycle a struct embedding a pointer to its own type creates;
	// without it the walk recurses until the stack runs out.
	seenTypes map[reflect.Type]bool
	excluded  []string
}

func (w *overrideWalk) walk(rt reflect.Type, namespace string) error {
	if w.seenTypes[rt] {
		return nil
	}
	w.seenTypes[rt] = true
	defer delete(w.seenTypes, rt)

	type embedded struct {
		rt   reflect.Type
		name string
	}
	embeddeds := make([]embedded, 0)

	// Wire names claimed within this one struct, for the collision check. Scoped per
	// level, because a shadowing field legitimately reuses the promoted field's wire
	// name — that is the case being excluded, not a conflict.
	wireNames := make(map[string]string)

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)

		if field.Anonymous {
			embeddedType := util.IndirectType(field.Type)
			if embeddedType != nil && embeddedType.Kind() == reflect.Struct {
				// Keyed by the field's own name, which Go defines as the embedded
				// type's name and which is populated for an embedded pointer too —
				// unlike reflect.Type.Name() on a pointer type.
				embeddeds = append(embeddeds, embedded{rt: embeddedType, name: field.Name})
			}
			continue
		}

		if !field.IsExported() {
			continue
		}

		// Two fields of one struct cannot share a Go name, so any collision here
		// necessarily involves an explicit tag.
		wire := model.WireFieldName(field)
		if previous, taken := wireNames[wire]; taken {
			return fmt.Errorf(
				"validator: %s has two fields on the wire name %q (%s and %s); the body "+
					"parser can only bind one of them, so give one a distinct json/scheme tag",
				rt, wire, previous, field.Name)
		}
		wireNames[wire] = field.Name

		if w.takenGoNames[field.Name] {
			w.excluded = append(w.excluded, join(namespace, field.Name))
		}
		w.takenGoNames[field.Name] = true
	}

	for _, e := range embeddeds {
		if err := w.walk(e.rt, join(namespace, e.name)); err != nil {
			return err
		}
	}

	return nil
}

func join(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}
