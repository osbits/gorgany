package validator

import (
	"encoding/json"
	"fmt"
	err2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/model"
	goValidator "github.com/go-playground/validator/v10"
	"mime"
	"reflect"
	"strconv"
	"strings"
)

func validateFile(field reflect.Value) interface{} {
	if field.Interface() == nil {
		return []byte("null")
	}

	if file, ok := field.Interface().(model.File); ok {
		if file.GetName() == "" && file.GetPath() == "" {
			return nil
		}

		content, err := json.Marshal(model.AbstractFile{
			Name: file.GetName(),
			Path: file.GetPath(),
		})
		if err != nil {
			return []byte("null")
		}
		return content
	}

	return []byte("null")
}

func validateMimeType(fl goValidator.FieldLevel) bool {
	if fl.Field().IsZero() {
		return true
	}

	fileContent, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	if string(fileContent) == "null" {
		return true
	}

	file := &model.AbstractFile{}
	err := json.Unmarshal(fileContent, file)
	if err != nil {
		err2.HandleError(fmt.Sprintf("Error when unmarshalling file content during validation: %v", err))
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
	if fl.Field().IsZero() {
		return true
	}

	fileContent, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}

	if string(fileContent) == "null" {
		return true
	}

	file := &model.AbstractFile{}
	err := json.Unmarshal(fileContent, file)
	if err != nil {
		err2.HandleError(fmt.Sprintf("Error when unmarshalling file content during validation: %v", err))
		return false
	}

	sizeInTag, err := strconv.ParseInt(fl.Param(), 10, 64)
	if err != nil {
		panic(err)
	}

	size, err := file.GetSize()
	if err != nil {
		return false
	}
	if size > sizeInTag {
		return false
	}
	return true
}
