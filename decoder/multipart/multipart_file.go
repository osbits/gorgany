package multipart

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/model"
	"io"
	"math/rand"
	"mime/multipart"
	"reflect"
	"strings"
	"time"
)

func DecodeFiles(filesMap map[string][]*multipart.FileHeader, dest any) ([]io.Closer, error) {
	reflectedDestVal := reflect.ValueOf(dest)

	openedFiles := make([]io.Closer, 0)
	for key, files := range filesMap {
		field := reflectedDestVal.Elem().FieldByNameFunc(func(n string) bool {
			return strings.ToLower(key) == strings.ToLower(n)
		})

		rawFile := files[0]
		reader, err := rawFile.Open()
		if err != nil {
			return openedFiles, err
		}
		openedFiles = append(openedFiles, reader)

		rand.Seed(time.Now().UnixNano())
		uniqueId := fmt.Sprintf("%d%d%d", rand.Intn(10000), rand.Intn(10000), rand.Intn(10000))
		file := model.File{
			Name:    uniqueId + "-" + rawFile.Filename,
			Content: reader,
			Size:    rawFile.Size,
			Loaded:  true,
		}
		field.Set(reflect.ValueOf(file))
	}

	return openedFiles, nil
}
