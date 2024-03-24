package service

import (
	"bytes"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/model"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

const RootStorage = "resource/public"

type FileService struct{}

func (f FileService) FullPath(file core.IFile) string {
	return path.Join(RootStorage, file.GetPath(), file.GetName())
}

func (f FileService) FullPathString(p string) string {
	return path.Join(RootStorage, p)
}

func (f FileService) PublicPath(file core.IFile) string {
	if file.GetPath() == "" && file.GetName() == "" {
		return ""
	}
	return path.Join("/", "public", file.GetPath(), file.GetName())
}

func (f FileService) Read(path string) (core.IFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	splitFullFilePath := strings.Split(path, "/")
	fileName := splitFullFilePath[len(splitFullFilePath)-1]
	p := strings.Join(splitFullFilePath[:len(splitFullFilePath)-1], "/")

	bReader := bytes.NewReader(content)
	file := &model.File{
		Name:    fileName,
		Path:    p,
		Content: io.NopCloser(bReader),
		Size:    int64(len(content)),
		Loaded:  true,
	}
	return file, nil
}

func (f FileService) Save(file core.IFile) error {
	if file.GetName() == "" || file.GetPath() == "" {
		return fmt.Errorf("Name and path must be specified in file")
	}
	name := fmt.Sprintf("%d_%s", time.Now().Unix(), file.GetName())
	file.SetName(name)

	p := f.FullPath(file)
	err := os.MkdirAll(path.Join(RootStorage, file.GetPath()), os.ModePerm)
	if err != nil {
		return err
	}

	rawFile, err := os.Create(p)
	if err != nil {
		return err
	}

	defer file.GetContent().Close()
	_, err = io.Copy(rawFile, file.GetContent())

	return err
}

func (f FileService) IsExists(file core.IFile) bool {
	_, err := os.ReadFile(f.FullPath(file))
	if err != nil {
		return false
	}
	return true
}

func (f FileService) Delete(p string) error {
	return os.Remove(p)
}

func (f FileService) DeleteFile(file core.IFile) error {
	if file.GetName() == "" {
		return nil
	}
	return os.Remove(f.FullPath(file))
}
