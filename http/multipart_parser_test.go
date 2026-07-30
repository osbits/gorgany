package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/err"
)

// TestStruct represents a simple struct for testing basic parsing
type TestStruct struct {
	Name     string     `scheme:"name"`
	Age      int        `scheme:"age"`
	Tags     []string   `scheme:"tags"`
	Optional *string    `scheme:"optional"`
	File     core.IFile `scheme:"file"`
	File1    core.IFile `scheme:"file1"`
	File2    core.IFile `scheme:"file2"`
}

type MultipartTestInterfaceStruct struct {
	Data   map[string]any `scheme:"data"`
	Values []any          `scheme:"values"`
	Rows   []any          `scheme:"rows"`
}

// TestDomainStruct represents a domain struct for testing domain parsing
type TestDomainStruct struct {
	ID   int    `scheme:"id" db:"id"`
	Name string `scheme:"name" db:"name"`
}

func (t *TestDomainStruct) GetLoaded() bool {
	return t.ID > 0
}

func (t *TestDomainStruct) GetID() int {
	return t.ID
}

// TestBindStruct represents a struct with bind methods
type TestBindStruct struct {
	CustomField string `scheme:"custom_field"`
}

func (t *TestBindStruct) BindCustomField(value string) error {
	if value == "invalid" {
		return &err.ValidationErrors{
			err.ValidationError{Field: "custom_field", Err: "invalid value"},
		}
	}
	t.CustomField = value
	return nil
}

func createMultipartForm(t *testing.T, values map[string]any, files map[string][]string) *http.Request {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add form values
	for key, val := range values {
		switch v := val.(type) {
		case []string:
			for _, s := range v {
				if err := writer.WriteField(key, s); err != nil {
					t.Fatalf("Failed to write field: %v", err)
				}
			}
		case string:
			if err := writer.WriteField(key, v); err != nil {
				t.Fatalf("Failed to write field: %v", err)
			}
		default:
			if err := writer.WriteField(key, fmt.Sprintf("%v", v)); err != nil {
				t.Fatalf("Failed to write field: %v", err)
			}
		}
	}

	// Add files
	for key, filenames := range files {
		for _, filename := range filenames {
			part, err := writer.CreateFormFile(key, filename)
			if err != nil {
				t.Fatalf("Failed to create form file: %v", err)
			}
			if _, err := part.Write([]byte("test content")); err != nil {
				t.Fatalf("Failed to write file content: %v", err)
			}
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("Failed to close writer: %v", err)
	}

	req, err := http.NewRequest("POST", "/", body)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestMultipartParser_Parse_Basic(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]any
		want    TestStruct
		wantErr bool
	}{
		{
			name: "valid basic struct",
			values: map[string]any{
				"name":     "John",
				"age":      "30",
				"tags":     []string{"tag1", "tag2"},
				"optional": "optional value",
			},
			want: TestStruct{
				Name:     "John",
				Age:      30,
				Tags:     []string{"tag1", "tag2"},
				Optional: stringPtr("optional value"),
			},
			wantErr: false,
		},
		{
			name: "invalid age",
			values: map[string]any{
				"name": "John",
				"age":  "invalid",
			},
			wantErr: true,
		},
		{
			name: "empty values",
			values: map[string]any{
				"name": "",
				"age":  "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createMultipartForm(t, tt.values, nil)
			parser := &MultipartParser{message: &mockHttpMessage{req: req}}

			var got TestStruct
			err := parser.Parse(&got)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if got.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", got.Name, tt.want.Name)
			}
			if got.Age != tt.want.Age {
				t.Errorf("Age = %v, want %v", got.Age, tt.want.Age)
			}
			if !compareStringSlices(got.Tags, tt.want.Tags) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.want.Tags)
			}
			if !compareStringPtrs(got.Optional, tt.want.Optional) {
				t.Errorf("Optional = %v, want %v", got.Optional, tt.want.Optional)
			}
		})
	}
}

func TestMultipartParser_Parse_InterfaceValues(t *testing.T) {
	req := createMultipartForm(t, map[string]any{
		"values":     []string{"181750", "false"},
		"rows[0][a]": "1",
		"rows[1][b]": "false",
	}, nil)
	parser := &MultipartParser{message: &mockHttpMessage{req: req}}

	var got MultipartTestInterfaceStruct
	if err := parser.Parse(&got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := parser.initStruct(&got, map[string]any{
		"data": map[string]any{
			"rows": []map[string]string{{"a": "1"}},
			"n":    "181750",
			"b":    "false",
			"null": nil,
		},
	}); err != nil {
		t.Fatalf("unexpected map binding error: %v", err)
	}

	wantValues := []any{"181750", "false"}
	if !reflect.DeepEqual(got.Values, wantValues) {
		t.Errorf("Values = %#v, want %#v", got.Values, wantValues)
	}

	wantRows := []any{
		map[string]string{"a": "1"},
		map[string]string{"b": "false"},
	}
	if !reflect.DeepEqual(got.Rows, wantRows) {
		t.Errorf("Rows = %#v, want %#v", got.Rows, wantRows)
	}

	wantData := map[string]any{
		"rows": []map[string]string{{"a": "1"}},
		"n":    "181750",
		"b":    "false",
		"null": nil,
	}
	if !reflect.DeepEqual(got.Data, wantData) {
		t.Errorf("Data = %#v, want %#v", got.Data, wantData)
	}
}

func TestMultipartParser_Parse_BindMethod(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]any
		want    TestBindStruct
		wantErr bool
	}{
		{
			name: "valid custom field",
			values: map[string]any{
				"custom_field": []string{"valid value"},
			},
			want: TestBindStruct{
				CustomField: "valid value",
			},
			wantErr: false,
		},
		{
			name: "invalid custom field",
			values: map[string]any{
				"custom_field": []string{"invalid"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createMultipartForm(t, tt.values, nil)
			parser := &MultipartParser{message: &mockHttpMessage{req: req}}

			var got TestBindStruct
			err := parser.Parse(&got)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if got.CustomField != tt.want.CustomField {
				t.Errorf("CustomField = %v, want %v", got.CustomField, tt.want.CustomField)
			}
		})
	}
}

func TestMultipartParser_Parse_Files(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]any
		files   map[string][]string
		wantErr bool
	}{
		{
			name: "valid file upload",
			values: map[string]any{
				"name": []string{"John"},
			},
			files: map[string][]string{
				"file": {"test.txt"},
			},
			wantErr: false,
		},
		{
			name: "multiple files",
			values: map[string]any{
				"name": []string{"John"},
			},
			files: map[string][]string{
				"file1": {"test1.txt"},
				"file2": {"test2.txt"},
			},
			wantErr: false,
		},
		{
			name: "file field not found",
			values: map[string]any{
				"name": []string{"John"},
			},
			files: map[string][]string{
				"file3": {"test1.txt"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createMultipartForm(t, tt.values, tt.files)
			parser := &MultipartParser{message: &mockHttpMessage{req: req}}

			var got TestStruct
			err := parser.Parse(&got)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func compareStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func compareStringPtrs(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// Mock HTTP message for testing
type mockHttpMessage struct {
	req *http.Request
}

func (m *mockHttpMessage) Request() core.IRequestScope {
	return &mockHttpRequest{req: m.req}
}

func (m *mockHttpMessage) Response() core.IResponseScope {
	return &mockHttpResponse{}
}

func (m *mockHttpMessage) Session() core.ISessionScope {
	return &mockSessionScope{}
}

func (m *mockHttpMessage) View() core.IViewScope {
	return &mockViewScope{}
}

func (m *mockHttpMessage) Cookie() core.ICookieManager {
	return &mockCookieManager{}
}

func (m *mockHttpMessage) Context() context.Context {
	return context.Background()
}

func (m *mockHttpMessage) Close() error {
	return nil
}

func (m *mockHttpMessage) RedirectWithFlash(url string, code int, data map[string]interface{}) {
	// Mock implementation
}

type mockHttpRequest struct {
	req *http.Request
}

func (r *mockHttpRequest) GetMultipartFormValues() *multipart.Form {
	err := r.req.ParseMultipartForm(32 << 20) // 32 MB
	if err != nil {
		return nil
	}
	return r.req.MultipartForm
}

// Implement other required IRequestScope methods
func (r *mockHttpRequest) Locale() string                                { return "" }
func (r *mockHttpRequest) PathParam(name string) string                  { return "" }
func (r *mockHttpRequest) QueryParam(key string) string                  { return "" }
func (r *mockHttpRequest) QueryParams(key string) []string               { return nil }
func (r *mockHttpRequest) QueryParamsMap(key string) []map[string]string { return nil }
func (r *mockHttpRequest) RawQuery() string                              { return "" }
func (r *mockHttpRequest) Header() http.Header                           { return nil }
func (r *mockHttpRequest) Body() ([]byte, error)                         { return nil, nil }
func (r *mockHttpRequest) BodyReader() io.ReadCloser                     { return nil }
func (r *mockHttpRequest) FormFile(key string) (core.IFile, error)       { return nil, nil }
func (r *mockHttpRequest) GetFiles(key string) ([]core.IFile, error)     { return nil, nil }
func (r *mockHttpRequest) IP() string                                    { return "" }
func (r *mockHttpRequest) Query() core.QueryParams                       { return nil }
func (r *mockHttpRequest) RawRequest() *http.Request                     { return r.req }

type mockHttpResponse struct{}

func (r *mockHttpResponse) SetHeader(key, value string)    {}
func (r *mockHttpResponse) Header() http.Header            { return nil }
func (r *mockHttpResponse) Text(body string, code int)     {}
func (r *mockHttpResponse) JSON(v interface{}, code int)   {}
func (r *mockHttpResponse) Bytes(b []byte, code int)       {}
func (r *mockHttpResponse) Redirect(url string, code int)  {}
func (r *mockHttpResponse) RawWriter() http.ResponseWriter { return nil }

type mockSessionScope struct{}

func (s *mockSessionScope) Setup()             {}
func (s *mockSessionScope) Get() core.ISession { return nil }
func (s *mockSessionScope) ClearExpiredFlash() {}

type mockViewScope struct{}

func (v *mockViewScope) Render(tpl string, data map[string]any) {}

type mockCookieManager struct{}

func (c *mockCookieManager) SetCookie(cookie *http.Cookie)     {}
func (c *mockCookieManager) GetCookie(key string) *http.Cookie { return nil }
func (c *mockCookieManager) GetCookies() []*http.Cookie        { return nil }
