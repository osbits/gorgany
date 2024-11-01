package validator

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/util"
	goValidator "github.com/go-playground/validator/v10"
	"reflect"
)

func GetValidator() core.IValidator {
	return internal.GetApplicationContext().GetValidator()
}

func New() core.IValidator {
	v := goValidator.New()

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

func (v *Validator) ValidateStruct(s any) error {
	overriddenFields := v.getOverriddenFields(s, "", nil)
	err := v.StructExcept(s, overriddenFields...)
	if err != nil {
		if _, ok := err.(*goValidator.InvalidValidationError); ok {
			return err
		}

		validationErrors := make(error2.ValidationErrors, 0)
		for _, e := range err.(goValidator.ValidationErrors) {
			validationErrors.AddValidationError(error2.ValidationError{
				Field: e.Field(),
				Err:   e.Error(),
			})
		}

		if len(validationErrors) > 0 {
			return &validationErrors
		}
	}

	return nil
}

func (v *Validator) getOverriddenFields(s any, parentKey string, parentFields map[string]bool) []string {
	if parentFields == nil {
		parentFields = make(map[string]bool)
	}

	rvS := util.IndirectValue(reflect.ValueOf(s))
	rtS := rvS.Type()

	embeddedFields := make(map[string]reflect.Value)

	overriddenFields := make([]string, 0)

	for i := 0; i < rvS.NumField(); i++ {
		rvField := rvS.Field(i)
		rtField := rtS.Field(i)
		if rtField.Anonymous {
			embeddedFields[rtField.Type.Name()] = rvField
			continue
		}

		if _, ok := parentFields[rtField.Name]; ok {
			overriddenFields = append(overriddenFields, parentKey+"."+rtField.Name)
		}

		parentFields[rtField.Name] = true
	}

	for fieldName, field := range embeddedFields {
		if parentKey == "" {
			parentKey = fieldName
		} else {
			parentKey = parentKey + "." + fieldName
		}
		overriddenFields = append(overriddenFields, v.getOverriddenFields(field.Addr().Interface(), parentKey, parentFields)...)
	}

	return overriddenFields
}
