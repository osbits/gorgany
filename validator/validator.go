package validator

import (
	goValidator "github.com/go-playground/validator/v10"
	error2 "gorgany/err"
	"gorgany/model"
)

func New() *goValidator.Validate {
	v := goValidator.New()

	v.RegisterCustomTypeFunc(validateFile, model.File{})
	err := v.RegisterValidation("mime", validateMimeType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("maxSize", validateFileSize)
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

	return v
}

func ValidateStruct(s any) error {
	validate := New()
	err := validate.Struct(s)
	if err != nil {
		if _, ok := err.(*goValidator.InvalidValidationError); ok {
			return err
		}

		validationErrors := error2.ValidationErrors{Errors: make([]error2.ValidationError, 0)}
		for _, err := range err.(goValidator.ValidationErrors) {
			validationErrors.Errors = append(validationErrors.Errors, error2.ValidationError{
				Field: err.Field(),
				Err:   err.Error(),
			})
		}

		if len(validationErrors.Errors) > 0 {
			return &validationErrors
		}
	}

	return nil
}
