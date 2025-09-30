package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	err2 "github.com/gorganyio/gorgany/err"
)

const (
	TempStorage   = "resource/temp"
	PublicStorage = "resource/public"
)

// ensureDirectories creates necessary directories if they don't exist
func ensureDirectories() error {
	dirs := []string{TempStorage, PublicStorage}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

func NewMultipartFile(originalFileName string, reader io.Reader) (*MultipartFile, error) {
	if err := ensureDirectories(); err != nil {
		return nil, err
	}

	// Generate a unique filename using timestamp and random number
	timestamp := time.Now().UnixNano()
	random := rand.Intn(10000)
	uniqueId := fmt.Sprintf("%d%d", timestamp, random)

	// Sanitize the original filename
	ext := filepath.Ext(originalFileName)
	baseName := strings.TrimSuffix(originalFileName, ext)
	sanitizedName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, baseName)

	fileName := fmt.Sprintf("%s-%s%s", uniqueId, sanitizedName, ext)

	file := MultipartFile{}
	file.SetName(fileName)

	// Create temp file
	tempPath := filepath.Join(TempStorage, fileName)
	tempFile, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Copy content to temp file
	if _, err := io.Copy(tempFile, reader); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}

	// Ensure the file is written to disk
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Reset file position for future reads
	if _, err := tempFile.Seek(0, 0); err != nil {
		tempFile.Close()
		os.Remove(tempPath) // Clean up on error
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	file.tempFile = tempFile
	return &file, nil
}

type AbstractFile struct {
	Name string
	Path string
}

func (thiz *AbstractFile) SetName(name string) {
	thiz.Name = name
}

func (thiz *AbstractFile) GetName() string {
	return thiz.Name
}

func (thiz *AbstractFile) GetPath() string {
	return thiz.Path
}

func (thiz *AbstractFile) GetSize() (int64, error) {
	info, err := os.Stat(thiz.FullPath())
	if err != nil {
		return 0, err
	}

	return info.Size(), nil
}

func (thiz *AbstractFile) FullPath() string {
	if thiz.Path == "" {
		return ""
	}
	return path.Join(PublicStorage, thiz.Path, thiz.Name)
}

// MultipartFile - this structure describes file which has been gotten by http, it can be stored on project server
type MultipartFile struct {
	AbstractFile
	tempFile *os.File
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
	if err != nil {
		return 0, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	return io.Copy(writer, file)
}

func (thiz *MultipartFile) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}

	// Ensure target directory exists
	targetDir := path.Join(PublicStorage, p)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create target directory: %w", err)
	}

	// If file already exists in a different location, delete it
	if thiz.Path != "" && thiz.Path != p {
		if err := thiz.Delete(); err != nil {
			return 0, fmt.Errorf("failed to delete existing file: %w", err)
		}
	}

	thiz.Path = p
	targetPath := path.Join(targetDir, thiz.Name)

	// Create the target file
	targetFile, err := os.Create(targetPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create target file: %w", err)
	}
	defer targetFile.Close()

	// Copy content
	var written int64
	if thiz.tempFile != nil {
		// Read from temp file
		if _, err := thiz.tempFile.Seek(0, 0); err != nil {
			return 0, fmt.Errorf("failed to seek temp file: %w", err)
		}
		written, err = io.Copy(targetFile, thiz.tempFile)
	} else {
		// Read from provided reader
		written, err = io.Copy(targetFile, reader)
	}

	if err != nil {
		os.Remove(targetPath) // Clean up on error
		return 0, fmt.Errorf("failed to write file: %w", err)
	}

	// Ensure the file is written to disk
	if err := targetFile.Sync(); err != nil {
		os.Remove(targetPath) // Clean up on error
		return 0, fmt.Errorf("failed to sync file: %w", err)
	}

	return written, nil
}

func (thiz *MultipartFile) Writer() (io.WriteCloser, error) {
	return os.Open(thiz.FullPath())
}

func (thiz *MultipartFile) FullPath() string {
	if thiz.Path == "" {
		return ""
	}
	return path.Join(PublicStorage, thiz.Path, thiz.Name)
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

	return os.Remove(thiz.FullPath())
}

func (thiz *MultipartFile) Close() error {
	if thiz.tempFile == nil {
		return nil
	}

	// Close the file
	if err := thiz.tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Get the temp file path
	tempPath := thiz.getActualFilePath()
	if tempPath == "" {
		return nil
	}

	// Remove the temp file
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("failed to remove temp file: %w", err)
	}

	return nil
}

// getActualFilePath - returns full path for temp file if permanent does not exist
func (thiz *MultipartFile) getActualFilePath() string {
	if thiz.FullPath() == "" {
		stat, err := thiz.tempFile.Stat()
		if err != nil {
			return ""
		}
		return path.Join(TempStorage, stat.Name())
	}

	return thiz.FullPath()
}

// File - this structure describes File which stores in project server storage and links with record in db
func NewFileFromMultipart(multipartFile MultipartFile) *File {
	return &File{
		MultipartFile: multipartFile,
	}
}

type File struct {
	MultipartFile
}

func (thiz *File) Write(p string, reader io.Reader) (int64, error) {
	if thiz.Name == "" {
		return 0, errors.New("file name is empty")
	}

	if thiz.FullPath() != "" && thiz.FullPath() != path.Join(PublicStorage, thiz.Path, thiz.Name) {
		err := thiz.Delete()
		if err != nil {
			return 0, err
		}
	}

	thiz.Path = p

	err := os.MkdirAll(path.Join(PublicStorage, thiz.Path), os.ModePerm)
	if err != nil {
		return 0, err
	}

	rawFile, err := os.Create(path.Join(PublicStorage, thiz.Path, thiz.Name))
	if err != nil {
		return 0, err
	}

	//thiz.StoredFilePath = path.Join(PublicStorage, thiz.Path, thiz.Name)

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
