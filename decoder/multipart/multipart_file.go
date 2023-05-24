package multipart

import (
	"gorgany/model"
	"io"
	"mime/multipart"
	"reflect"
)

func DecodeFiles(filesMap map[string][]*multipart.FileHeader, dest any) error {
	reflectedDestVal := reflect.ValueOf(dest)

	for key, files := range filesMap {
		field := reflectedDestVal.Elem().FieldByName(key)
		rawFile := files[0]
		reader, err := rawFile.Open()
		if err != nil {
			return err
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		file := model.File{
			Name:    rawFile.Filename,
			Content: string(content),
		}
		field.Set(reflect.ValueOf(file))
	}

	return nil
}
