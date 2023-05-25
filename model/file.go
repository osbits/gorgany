package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

const RootStorage = "resource/public"

type File struct {
	Name    string
	Path    string
	Content string
	Size    int64
}

func (thiz File) FullPath() string {
	return path.Join(RootStorage, thiz.Path, thiz.Name)
}

func (thiz File) PublicPath() string {
	if thiz.Path == "" && thiz.Name == "" {
		return ""
	}
	return path.Join("/", "public", thiz.Path, thiz.Name)
}

func (thiz File) PathInPublic() string {
	return path.Join(thiz.Path, thiz.Name)
}

func (thiz File) ReadContent() (string, error) {
	file, err := os.ReadFile(thiz.FullPath())
	if err != nil {
		return "", err
	}
	return string(file), nil
}

func (thiz *File) Save(p string) error {
	if thiz.Name == "" {
		return nil
	}

	if p == "" {
		p = thiz.Path
	} else if thiz.Path == "" {
		thiz.Path = p
	}

	p = path.Join(RootStorage, p)
	err := os.MkdirAll(p, os.ModePerm)
	if err != nil {
		return err
	}

	file, err := os.Create(path.Join(p, thiz.Name))
	if err != nil {
		return err
	}

	file.Write([]byte(thiz.Content))
	return nil
}

func (thiz File) IsExists() bool {
	stat, err := os.Stat(thiz.FullPath())
	if err != nil {
		return false
	}
	return !stat.IsDir()
}

func (thiz File) IsNil() bool {
	if thiz.Name == "" {
		return true
	}
	return false
}

func (thiz *File) Delete() error {
	if !thiz.IsExists() {
		return nil
	}

	if thiz.FullPath() == "" {
		return nil
	}

	err := os.Remove(thiz.FullPath())
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") {
			return nil
		}
		return err
	}

	thiz.Content = ""
	thiz.Name = ""
	thiz.Path = ""

	return nil
}

func (thiz *File) Scan(value interface{}) error {
	fullFilePath, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to cast value:", value))
	}

	splitFullFilePath := strings.Split(fullFilePath, "/")
	fileName := splitFullFilePath[len(splitFullFilePath)-1]
	p := strings.Join(splitFullFilePath[:len(splitFullFilePath)-1], "/")

	thiz.Path = p
	thiz.Name = fileName

	content, err := thiz.ReadContent()
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") {
			return nil
		}
		return err
	}
	thiz.Content = content

	return nil
}

func (thiz File) Value() (driver.Value, error) {
	if thiz.PathInPublic() == "" || !thiz.IsExists() {
		return nil, nil
	}
	return thiz.PathInPublic(), nil
}

func (thiz File) MarshalJSON() ([]byte, error) {
	if thiz.Name == "" {
		return []byte("{}"), nil
	}

	fileMap := make(map[string]any)
	fileMap["Name"] = thiz.Name
	fileMap["Path"] = thiz.Path
	fileMap["Size"] = thiz.Size

	jsonFile, err := json.Marshal(fileMap)
	if err != nil {
		return nil, nil
	}
	return jsonFile, nil
}
