package validator

import (
	"encoding/json"
	goValidator "github.com/go-playground/validator/v10"
	"gorgany/model"
	"mime"
	"reflect"
	"strconv"
	"strings"
)

func validateFile(field reflect.Value) interface{} {
	if file, ok := field.Interface().(model.File); ok {
		jsonFile, err := json.Marshal(file)
		if err != nil {
			return nil
		}
		return jsonFile
	}
	return nil
}

func validateMimeType(fl goValidator.FieldLevel) bool {
	fileJson, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	if string(fileJson) == "{}" {
		return true
	}

	file := &model.File{}
	err := json.Unmarshal(fileJson, file)
	if err != nil {
		return false
	}

	splitName := strings.Split(file.Name, ".")
	m := mime.TypeByExtension("." + splitName[len(splitName)-1])
	params := strings.Split(fl.Param(), ";")
	for _, param := range params {
		if param == m {
			return true
		}
	}
	return false
}

func validateFileSize(fl goValidator.FieldLevel) bool {
	fileJson, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	if fileJson == nil {
		return true
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
