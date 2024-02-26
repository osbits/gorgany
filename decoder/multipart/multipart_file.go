package multipart

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/model"
	"io"
	"math/rand"
	"mime/multipart"
	"reflect"
	"time"
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

		rand.Seed(time.Now().UnixNano())
		uniqueId := fmt.Sprintf("%d%d%d", rand.Intn(10000), rand.Intn(10000), rand.Intn(10000))
		file := model.File{
			Name:    uniqueId + "-" + rawFile.Filename,
			Content: string(content),
			Size:    rawFile.Size,
		}
		field.Set(reflect.ValueOf(file))
	}

	return nil
}
