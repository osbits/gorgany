package validator

import (
	"encoding/json"
	"github.com/gabriel-vasile/mimetype"
	goValidator "github.com/go-playground/validator/v10"
	"gorgany/model"
	"reflect"
	"strconv"
)

func ValidateFile(field reflect.Value) interface{} {
	if file, ok := field.Interface().(model.File); ok {
		jsonFile, err := json.Marshal(file)
		if err != nil {
			return nil
		}
		return jsonFile
	}
	return nil
}

func ValidateMimeType(fl goValidator.FieldLevel) bool {
	fileJson, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	file := &model.File{}
	err := json.Unmarshal(fileJson, file)
	if err != nil {
		return false
	}

	m := mimetype.Detect([]byte(file.Content))
	if fl.Param() == m.String() {
		return true
	}
	return false
}

func ValidateFileSize(fl goValidator.FieldLevel) bool {
	fileJson, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	file := &model.File{}
	err := json.Unmarshal(fileJson, file)
	if err != nil {
		return false
	}

	sizeInTag, err := strconv.ParseInt(fl.Param(), 10, 64)
	if err != nil {
		panic(err)
	}

	if file.Size > sizeInTag {
		return false
	}
	return true
}
