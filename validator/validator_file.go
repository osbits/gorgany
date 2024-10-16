package validator

import (
	"encoding/json"
	"fmt"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/model"
	goValidator "github.com/go-playground/validator/v10"
	"mime"
	"reflect"
	"strconv"
	"strings"
)

func validateFile(field reflect.Value) interface{} {
	if field.Interface() == nil {
		return nil
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
			return nil
		}
		return content
	}

	return nil
	//if file, ok := field.Interface().(core.IFile); ok {
	//	jsonFile, err := json.Marshal(file)
	//	if err != nil {
	//		return nil
	//	}
	//	return jsonFile
	//}
	//return nil
}

func validateMimeType(fl goValidator.FieldLevel) bool {
	if fl.Field().Interface() == nil {
		return true
	}

	fileContent, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
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
	if fl.Field().Interface() == nil {
		return true
	}

	file, ok := fl.Field().Interface().(model.AbstractFile)
	if !ok {
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
