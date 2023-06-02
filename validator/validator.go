package validator

import (
	goValidator "github.com/go-playground/validator/v10"
	"gorgany/model"
)

func New() *goValidator.Validate {
	v := goValidator.New()

	v.RegisterCustomTypeFunc(ValidateFile, model.File{})
	err := v.RegisterValidation("mime", ValidateMimeType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("maxSize", ValidateFileSize)
	if err != nil {
		panic(err)
	}

	v.RegisterCustomTypeFunc(ValidateLocalizedString, model.LocalizedString{})
	err = v.RegisterValidation("lsCompletelyRequired", ValidateRequiredLocalizedString) //all langs in LocalizedString must not be empty
	if err != nil {
		panic(err)
	}

	return v
}
