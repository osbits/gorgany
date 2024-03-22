package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

type File struct {
	Name    string
	Path    string
	Content []byte
	Size    int64
	Loaded  bool // if file has been read and contains content
}

func (thiz *File) SetName(name string) {
	thiz.Name = name
}

func (thiz *File) GetName() string {
	return thiz.Name
}

func (thiz *File) GetSize() int64 {
	return thiz.Size
}

func (thiz *File) GetContent() []byte {
	return thiz.Content
}

func (thiz *File) GetPath() string {
	return thiz.Path
}

func (thiz *File) IsEmpty() bool {
	if thiz.Name == "" {
		return true
	}
	return false
}

func (thiz *File) IsLoaded() bool {
	return thiz.Loaded
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
	thiz.Loaded = false

	return nil
}

func (thiz File) Value() (driver.Value, error) {
	if thiz.Path == "" || thiz.Name == "" {
		return nil, nil
	}
	return path.Join(thiz.Path, thiz.Name), nil
}

func (thiz File) MarshalJSON() ([]byte, error) {
	if thiz.Name == "" {
		return []byte("null"), nil
	}

	fileMap := make(map[string]any)
	fileMap["name"] = thiz.Name
	//fileMap["Size"] = thiz.Size todo: fix it

	jsonFile, err := json.Marshal(fileMap)
	if err != nil {
		return nil, nil
	}
	return jsonFile, nil
}
