package model

import (
	"bytes"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbstractFile(t *testing.T) {
	// Setup
	file := &AbstractFile{
		Name: "test.txt",
		Path: "test/path",
	}

	// Test SetName
	file.SetName("new.txt")
	if file.Name != "new.txt" {
		t.Errorf("SetName failed, expected 'new.txt', got '%s'", file.Name)
	}

	// Test GetName
	if name := file.GetName(); name != "new.txt" {
		t.Errorf("GetName failed, expected 'new.txt', got '%s'", name)
	}

	// Test GetPath
	if path := file.GetPath(); path != "test/path" {
		t.Errorf("GetPath failed, expected 'test/path', got '%s'", path)
	}

	// Test FullPath
	expectedPath := filepath.Join(PublicStorage, "test/path", "new.txt")
	if fullPath := file.FullPath(); fullPath != expectedPath {
		t.Errorf("FullPath failed, expected '%s', got '%s'", expectedPath, fullPath)
	}

	// Test GetSize with non-existent file
	_, err := file.GetSize()
	if err == nil {
		t.Error("GetSize should return error for non-existent file")
	}
}

func TestMultipartFile(t *testing.T) {
	// Setup test content
	content := []byte("test content")
	reader := bytes.NewReader(content)

	// Test NewMultipartFile
	file, err := NewMultipartFile("test.txt", reader)
	if err != nil {
		t.Fatalf("NewMultipartFile failed: %v", err)
	}
	defer file.Close()

	// Verify file was created
	if file.Name == "" {
		t.Error("NewMultipartFile did not set file name")
	}

	// Test Write
	testPath := "test/path"
	written, err := file.Write(testPath, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if written != int64(len(content)) {
		t.Errorf("Write failed, expected %d bytes written, got %d", len(content), written)
	}

	// Test IsExists
	if !file.IsExists() {
		t.Error("IsExists failed, file should exist")
	}

	// Test GetSize
	size, err := file.GetSize()
	if err != nil {
		t.Fatalf("GetSize failed: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("GetSize failed, expected %d, got %d", len(content), size)
	}

	// Test Read
	var buf bytes.Buffer
	read, err := file.Read(&buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if read != int64(len(content)) {
		t.Errorf("Read failed, expected %d bytes read, got %d", len(content), read)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Error("Read failed, content mismatch")
	}

	// Test PublicPath.
	//
	// A rooted URL with one `public` segment. It used to be relative and to be one segment
	// short of what the route wanted — the working URL for a stored upload was
	// /public/public/<path>/<name> — so both halves of that changed together; see
	// PublicPath and TestPublicPathRoundTripsThroughTheRouteExactlyOnce.
	expectedPublicPath := "/" + path.Join("public", testPath, file.Name)
	if publicPath := file.PublicPath(); publicPath != expectedPublicPath {
		t.Errorf("PublicPath failed, expected '%s', got '%s'", expectedPublicPath, publicPath)
	}

	// Test Delete
	if err := file.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if file.IsExists() {
		t.Error("Delete failed, file still exists")
	}
}

func TestFile(t *testing.T) {
	// Setup test content
	content := []byte("test content")
	reader := bytes.NewReader(content)

	// Create MultipartFile first
	multipartFile, err := NewMultipartFile("test.txt", reader)
	if err != nil {
		t.Fatalf("NewMultipartFile failed: %v", err)
	}
	defer multipartFile.Close()

	// Test NewFileFromMultipart
	file := NewFileFromMultipart(*multipartFile)

	// Test Write
	testPath := "test/path"
	written, err := file.Write(testPath, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if written != int64(len(content)) {
		t.Errorf("Write failed, expected %d bytes written, got %d", len(content), written)
	}

	// Test Scan
	err = file.Scan(filepath.Join(testPath, file.Name))
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if file.Path != testPath {
		t.Errorf("Scan failed, expected path '%s', got '%s'", testPath, file.Path)
	}

	// Test Value
	value, err := file.Value()
	if err != nil {
		t.Fatalf("Value failed: %v", err)
	}
	expectedValue := filepath.Join(testPath, file.Name)
	if value != expectedValue {
		t.Errorf("Value failed, expected '%s', got '%s'", expectedValue, value)
	}

	// Test MarshalJSON
	jsonData, err := file.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	if !strings.Contains(string(jsonData), file.Name) {
		t.Error("MarshalJSON failed, JSON does not contain file name")
	}

	// Cleanup
	if err := file.Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestFileSanitization(t *testing.T) {
	// Test with special characters in filename
	content := []byte("test content")
	reader := bytes.NewReader(content)

	file, err := NewMultipartFile("test@#$%^&*.txt", reader)
	if err != nil {
		t.Fatalf("NewMultipartFile failed: %v", err)
	}
	defer file.Close()

	// Verify filename was sanitized
	if strings.ContainsAny(file.Name, "@#$%^&*") {
		t.Error("Filename was not properly sanitized")
	}
}

func TestFileErrorCases(t *testing.T) {
	// Test with empty filename
	file := &MultipartFile{}
	_, err := file.Write("test/path", bytes.NewReader([]byte("test")))
	if err == nil {
		t.Error("Write should fail with empty filename")
	}

	// Test with non-existent file
	file = &MultipartFile{
		AbstractFile: AbstractFile{
			Name: "nonexistent.txt",
			Path: "test/path",
		},
	}
	_, err = file.GetSize()
	if err == nil {
		t.Error("GetSize should fail for non-existent file")
	}

	// Test Delete on non-existent file
	err = file.Delete()
	if err == nil {
		t.Error("Delete should fail for non-existent file")
	}
}
