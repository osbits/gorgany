package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const RootStorage = "resource/public"

// MultipartFile - this structure describes file which has been gotten by http, it can be stored on project server
type MultipartFile struct {
	Name    string
	Path    string
	Content io.ReadCloser
	Size    int64
	Read    bool
	Saved   bool
}

func (thiz *MultipartFile) SetName(name string) {
	thiz.Name = name
}

func (thiz *MultipartFile) GetName() string {
	return thiz.Name
}

func (thiz *MultipartFile) GetSize() int64 {
	return thiz.Size
}

func (thiz *MultipartFile) GetContent() (io.ReadCloser, error) {
	return thiz.Content, nil
}

func (thiz *MultipartFile) GetPath() string {
	return thiz.Path
}

func (thiz *MultipartFile) IsRead() bool {
	return thiz.Read
}

func (thiz *MultipartFile) IsSaved() bool {
	return thiz.Saved
}

func (thiz *MultipartFile) SetContent(content []byte) error {
	thiz.Content = io.NopCloser(bytes.NewBuffer(content))
	thiz.Size = int64(len(content))
	return nil
}

func (thiz *MultipartFile) FullPath() string {
	return path.Join(RootStorage, thiz.GetPath(), thiz.Name)
}

func (thiz *MultipartFile) PublicPath() string {
	return path.Join("public", thiz.Path, thiz.Name)
}

func (thiz *MultipartFile) Save(p string) error {
	if thiz.Name == "" {
		return errors.New("file name is empty")
	}
	thiz.Path = p

	err := os.MkdirAll(path.Join(RootStorage, thiz.GetPath()), os.ModePerm)
	if err != nil {
		return err
	}

	rawFile, err := os.Create(thiz.FullPath())
	if err != nil {
		return err
	}

	if thiz.Content != nil {
		_, err = io.Copy(rawFile, thiz.Content)
	}

	thiz.Saved = true

	return nil
}

func (thiz *MultipartFile) IsExists() bool {
	_, err := os.Stat(thiz.FullPath())
	return err == nil
}

func (thiz *MultipartFile) Delete() error {
	if thiz.Name == "" {
		return errors.New("file name is empty")
	}

	return os.Remove(thiz.FullPath())
}

// File - this structure describes File which stores in project server storage and links with record in db
func NewFileFromMultipart(multipartFile MultipartFile) File {
	return File{
		MultipartFile: multipartFile,
	}
}

type File struct {
	MultipartFile
}

func (thiz *File) GetContent() (io.ReadCloser, error) {
	if thiz.Content == nil {
		content, err := thiz.readContent()
		if err != nil {
			return nil, err
		}
		thiz.Content = content
	}
	return thiz.Content, nil
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

	content, err := thiz.readContent()
	if err != nil {
		return err
	}
	thiz.Read = true
	thiz.Content = content

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
	fileMap["size"] = thiz.Size
	fileMap["path"] = thiz.PublicPath()

	jsonFile, err := json.Marshal(fileMap)
	if err != nil {
		return nil, nil
	}
	return jsonFile, nil
}

func (thiz *File) readContent() (io.ReadCloser, error) {
	content, err := os.ReadFile(thiz.FullPath())
	if err != nil {
		return nil, err
	}

	thiz.Read = true
	thiz.Size = int64(len(content))
	return io.NopCloser(bytes.NewReader(content)), nil
}
