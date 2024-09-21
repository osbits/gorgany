package model

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"io"
	"math/rand"
	"os"
	"path"
	"strings"
)

const TempStorage = "resource/temp"
const PublicStorage = "resource/public"

func NewMultipartFile(originalFileName string, reader io.Reader) (*MultipartFile, error) {
	uniqueId := fmt.Sprintf("%d%d%d", rand.Intn(10000), rand.Intn(10000), rand.Intn(10000))

	file := MultipartFile{}
	file.SetName(uniqueId + "-" + originalFileName)

	tempFile, err := os.CreateTemp(TempStorage, file.GetName()+".tmp")
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(tempFile, reader)
	if err != nil {
		return nil, err
	}

	file.tempFile = tempFile

	return &file, nil
}

// MultipartFile - this structure describes file which has been gotten by http, it can be stored on project server
type MultipartFile struct {
	Name           string
	Path           string
	StoredFilePath string
	tempFile       *os.File
}

func (thiz *MultipartFile) SetName(name string) {
	thiz.Name = name
}

func (thiz *MultipartFile) GetName() string {
	return thiz.Name
}

func (thiz *MultipartFile) GetPath() string {
	return thiz.Path
}

func (thiz *MultipartFile) GetSize() (int64, error) {
	actualFilePath := thiz.getActualFilePath()
	if actualFilePath == "" {
		return 0, fmt.Errorf("no file path")
	}

	info, err := os.Stat(actualFilePath)
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

func (thiz *MultipartFile) Read(writer io.Writer) (int64, error) {
	actualFilePath := thiz.getActualFilePath()
	if actualFilePath == "" {
		return 0, fmt.Errorf("no file path")
	}

	file, err := os.Open(actualFilePath)
	defer file.Close()

	if err != nil {
		return 0, err
	}

	return io.Copy(writer, file)
}

func (thiz *MultipartFile) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}
	thiz.Path = p

	if thiz.StoredFilePath != "" && thiz.StoredFilePath != path.Join(PublicStorage, thiz.Path, thiz.Name) {
		err := thiz.Delete()
		if err != nil {
			return 0, err
		}
	}

	err := os.MkdirAll(path.Join(PublicStorage, thiz.Path), os.ModePerm)
	if err != nil {
		return 0, err
	}

	rawFile, err := os.Create(path.Join(PublicStorage, thiz.Path, thiz.Name))
	if err != nil {
		return 0, err
	}

	// We consider that permanent file has not been saved yet, so we are reading content from temp
	buf := new(bytes.Buffer)
	_, err = thiz.Read(buf)
	if err != nil {
		return 0, err
	}

	written, err := io.Copy(rawFile, buf)
	if err != nil {
		return 0, err
	}

	thiz.StoredFilePath = path.Join(PublicStorage, thiz.Path, thiz.Name)

	return written, nil
}

func (thiz *MultipartFile) Writer() (io.WriteCloser, error) {
	return os.Open(thiz.FullPath())
}

func (thiz *MultipartFile) FullPath() string {
	return thiz.StoredFilePath
}

func (thiz *MultipartFile) PublicPath() string {
	return path.Join("public", thiz.Path, thiz.Name)
}

func (thiz *MultipartFile) IsExists() bool {
	if thiz.FullPath() == "" {
		return false
	}
	_, err := os.Stat(thiz.FullPath())
	return err == nil
}

func (thiz *MultipartFile) Delete() error {
	if thiz.Name == "" {
		return errors.New("file name is empty")
	}

	if !thiz.IsExists() {
		return errors.New("file does not exist")
	}

	return os.Remove(thiz.StoredFilePath)
}

func (thiz *MultipartFile) Close() error {
	stat, err := thiz.tempFile.Stat()
	if err != nil {
		return err
	}
	return os.Remove(path.Join(TempStorage, stat.Name()))
}

// File - this structure describes File which stores in project server storage and links with record in db
func NewFileFromMultipart(multipartFile MultipartFile) File {
	return File{
		MultipartFile: multipartFile,
	}
}

// getActualFilePath - returns full path for temp file if permanent does not exist
func (thiz *MultipartFile) getActualFilePath() string {
	if thiz.StoredFilePath == "" {
		stat, err := thiz.tempFile.Stat()
		if err != nil {
			return ""
		}
		return path.Join(TempStorage, stat.Name())
	}

	return thiz.StoredFilePath
}

type File struct {
	MultipartFile
}

func (thiz *File) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}
	thiz.Path = p

	if thiz.StoredFilePath != "" && thiz.StoredFilePath != path.Join(PublicStorage, thiz.Path, thiz.Name) {
		err := thiz.Delete()
		if err != nil {
			return 0, err
		}
	}

	err := os.MkdirAll(path.Join(PublicStorage, thiz.Path), os.ModePerm)
	if err != nil {
		return 0, err
	}

	rawFile, err := os.Create(path.Join(PublicStorage, thiz.Path, thiz.Name))
	if err != nil {
		return 0, err
	}

	thiz.StoredFilePath = path.Join(PublicStorage, thiz.Path, thiz.Name)

	return io.Copy(rawFile, reader)
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

	size, err := thiz.GetSize()
	if err != nil {
		err2.HandleError(err)
		return []byte("null"), nil
	}

	fileMap := make(map[string]any)
	fileMap["name"] = thiz.Name
	fileMap["size"] = size
	fileMap["path"] = thiz.PublicPath()

	jsonFile, err := json.Marshal(fileMap)
	if err != nil {
		return nil, nil
	}
	return jsonFile, nil
}

func (thiz *File) Close() error {
	return nil
}
