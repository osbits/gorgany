package multipart

import (
	"github.com/osbits/gorgany/v2/model"
	"io"
	"mime/multipart"
	"reflect"
	"strings"
)

func DecodeFiles(filesMap map[string][]*multipart.FileHeader, dest any) ([]io.Closer, error) {
	reflectedDestVal := reflect.ValueOf(dest)

	openedFiles := make([]io.Closer, 0)
	for key, files := range filesMap {
		field := reflectedDestVal.Elem().FieldByNameFunc(func(n string) bool {
			return strings.ToLower(key) == strings.ToLower(n)
		})

		if !field.IsValid() {
			continue
		}

		rawFile := files[0]
		reader, err := rawFile.Open()
		if err != nil {
			return openedFiles, err
		}
		openedFiles = append(openedFiles, reader)

		file, err := model.NewMultipartFile(rawFile.Filename, reader)

		openedFiles = append(openedFiles, file)

		field.Set(reflect.ValueOf(file))
	}

	return openedFiles, nil
}
