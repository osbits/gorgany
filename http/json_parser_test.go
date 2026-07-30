package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
)

// JSONTestStruct represents a simple struct for testing basic JSON parsing
type JSONTestStruct struct {
	Name     string         `json:"name"`
	Age      int            `json:"age"`
	Tags     []string       `json:"tags"`
	Optional *string        `json:"optional"`
	Data     JSONTestNested `json:"data"`
}

// JSONTestNested represents a nested struct for testing complex JSON parsing
type JSONTestNested struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// JSONTestInterfaceStruct represents interface-valued JSON command fields.
type JSONTestInterfaceStruct struct {
	Data  map[string]any `json:"data"`
	Items []any          `json:"items"`
}

type JSONTestNonEmptyInterfaceStruct struct {
	Value fmt.Stringer `json:"value"`
}

// JSONTestDomainStruct represents a domain struct for testing JSON domain parsing
type JSONTestDomainStruct struct {
	ID   int    `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

func (t *JSONTestDomainStruct) GetLoaded() bool {
	return t.ID > 0
}

func (t *JSONTestDomainStruct) GetID() int {
	return t.ID
}

// JSONTestBindStruct represents a struct with bind methods for JSON parsing
type JSONTestBindStruct struct {
	CustomField string `json:"custom_field"`
}

func (t *JSONTestBindStruct) BindCustomField(value string) error {
	if value == "invalid" {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: "custom_field", Err: "invalid value"},
		}
	}
	t.CustomField = value
	return nil
}

func createJSONRequest(t *testing.T, body interface{}) *http.Request {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Failed to marshal test data: %v", err)
		}
		bodyReader = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequest("POST", "/", bodyReader)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestJsonParser_Parse_Basic(t *testing.T) {
	tests := []struct {
		name    string
		body    interface{}
		want    JSONTestStruct
		wantErr bool
	}{
		{
			name: "valid basic struct",
			body: map[string]interface{}{
				"name":     "John",
				"age":      30,
				"tags":     []string{"tag1", "tag2"},
				"optional": "optional value",
				"data": map[string]interface{}{
					"value": "test",
					"count": 42,
				},
			},
			want: JSONTestStruct{
				Name:     "John",
				Age:      30,
				Tags:     []string{"tag1", "tag2"},
				Optional: stringPtr("optional value"),
				Data: JSONTestNested{
					Value: "test",
					Count: 42,
				},
			},
			wantErr: false,
		},
		{
			name: "invalid age type",
			body: map[string]interface{}{
				"name": "John",
				"age":  "invalid",
			},
			wantErr: true,
		},
		{
			name: "empty values",
			body: map[string]interface{}{
				"name": "",
				"age":  0,
			},
			want: JSONTestStruct{
				Name: "",
				Age:  0,
			},
			wantErr: false,
		},
		{
			name:    "empty body",
			body:    nil,
			want:    JSONTestStruct{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createJSONRequest(t, tt.body)
			parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

			var got JSONTestStruct
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
			if got.Data.Value != tt.want.Data.Value {
				t.Errorf("Data.Value = %v, want %v", got.Data.Value, tt.want.Data.Value)
			}
			if got.Data.Count != tt.want.Data.Count {
				t.Errorf("Data.Count = %v, want %v", got.Data.Count, tt.want.Data.Count)
			}
		})
	}
}

func TestJsonParser_Parse_InterfaceValues(t *testing.T) {
	body := map[string]any{
		"data": map[string]any{
			"arr":  []any{map[string]any{"a": 1}},
			"n":    181750,
			"b":    false,
			"null": nil,
		},
		"items": []any{
			map[string]any{"a": 1},
			[]any{"nested"},
			181750,
			false,
			nil,
		},
	}
	req := createJSONRequest(t, body)
	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

	var got JSONTestInterfaceStruct
	if err := parser.Parse(&got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantData := map[string]any{
		"arr":  []any{map[string]any{"a": float64(1)}},
		"n":    float64(181750),
		"b":    false,
		"null": nil,
	}
	if !reflect.DeepEqual(got.Data, wantData) {
		t.Errorf("Data = %#v, want %#v", got.Data, wantData)
	}

	wantItems := []any{
		map[string]any{"a": float64(1)},
		[]any{"nested"},
		float64(181750),
		false,
		nil,
	}
	if !reflect.DeepEqual(got.Items, wantItems) {
		t.Errorf("Items = %#v, want %#v", got.Items, wantItems)
	}
}

func TestJsonParser_Parse_UnassignableInterfaceValue(t *testing.T) {
	req := createJSONRequest(t, map[string]any{"value": "not a stringer"})
	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

	var got JSONTestNonEmptyInterfaceStruct
	err := parser.Parse(&got)
	if err == nil {
		t.Fatal("expected an error for a value that does not implement fmt.Stringer")
	}
	if _, ok := err.(*error2.ValidationErrors); !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
}

func TestJsonParser_Parse_BindMethod(t *testing.T) {
	tests := []struct {
		name    string
		body    interface{}
		want    JSONTestBindStruct
		wantErr bool
	}{
		{
			name: "valid custom field",
			body: map[string]interface{}{
				"custom_field": "valid value",
			},
			want: JSONTestBindStruct{
				CustomField: "valid value",
			},
			wantErr: false,
		},
		{
			name: "invalid custom field",
			body: map[string]interface{}{
				"custom_field": "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createJSONRequest(t, tt.body)
			parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

			var got JSONTestBindStruct
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

func TestJsonParser_Parse_SizeLimit(t *testing.T) {
	// Create a large JSON payload
	largeBody := make(map[string]interface{})
	for i := 0; i < 1000000; i++ {
		largeBody[fmt.Sprintf("key%d", i)] = fmt.Sprintf("value%d", i)
	}

	req := createJSONRequest(t, largeBody)
	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

	var got JSONTestStruct
	err := parser.Parse(&got)

	if err == nil {
		t.Error("expected error for large payload but got none")
	}

	// This used to assert *ValidationErrors. A refusal to parse is a 400-class failure
	// with no field to name, so it is now an InputBodyParseError (B3) — which is also
	// the type http/error.go had a handler registered for all along.
	parseError, ok := err.(*error2.InputBodyParseError)
	if !ok {
		t.Fatalf("expected InputBodyParseError but got %T", err)
	}

	if parseError.RawError == nil {
		t.Error("expected a reason attached to the parse error")
	}
}

func TestJsonParser_Parse_DepthLimit(t *testing.T) {
	// Create a deeply nested JSON structure
	deepBody := make(map[string]interface{})
	current := deepBody
	for i := 0; i < 50; i++ {
		next := make(map[string]interface{})
		current["nested"] = next
		current = next
	}

	req := createJSONRequest(t, deepBody)
	parser := &JsonParser{message: &jsonMockHttpMessage{req: req}}

	var got JSONTestStruct
	err := parser.Parse(&got)

	if err == nil {
		t.Error("expected error for deep structure but got none")
	}

	// Same reclassification as the size limit above (B3).
	parseError, ok := err.(*error2.InputBodyParseError)
	if !ok {
		t.Fatalf("expected InputBodyParseError but got %T", err)
	}

	if parseError.RawError == nil {
		t.Error("expected a reason attached to the parse error")
	}
}

// Mock HTTP message for testing
type jsonMockHttpMessage struct {
	req *http.Request
}

func (m *jsonMockHttpMessage) Request() core.IRequestScope {
	return &jsonMockHttpRequest{req: m.req}
}

func (m *jsonMockHttpMessage) Response() core.IResponseScope {
	return &jsonMockHttpResponse{}
}

func (m *jsonMockHttpMessage) Session() core.ISessionScope {
	return &jsonMockSessionScope{}
}

func (m *jsonMockHttpMessage) View() core.IViewScope {
	return &jsonMockViewScope{}
}

func (m *jsonMockHttpMessage) Cookie() core.ICookieManager {
	return &jsonMockCookieManager{}
}

func (m *jsonMockHttpMessage) Context() context.Context {
	return context.Background()
}

func (m *jsonMockHttpMessage) Close() error {
	return nil
}

func (m *jsonMockHttpMessage) RedirectWithFlash(url string, code int, data map[string]interface{}) {
	// Mock implementation
}

type jsonMockHttpRequest struct {
	req *http.Request
}

func (r *jsonMockHttpRequest) Body() ([]byte, error) {
	if r.req.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.req.Body)
}

// Implement other required IRequestScope methods
func (r *jsonMockHttpRequest) Locale() string                                { return "" }
func (r *jsonMockHttpRequest) PathParam(name string) string                  { return "" }
func (r *jsonMockHttpRequest) QueryParam(key string) string                  { return "" }
func (r *jsonMockHttpRequest) QueryParams(key string) []string               { return nil }
func (r *jsonMockHttpRequest) QueryParamsMap(key string) []map[string]string { return nil }
func (r *jsonMockHttpRequest) RawQuery() string                              { return "" }
func (r *jsonMockHttpRequest) Header() http.Header                           { return nil }
func (r *jsonMockHttpRequest) BodyReader() io.ReadCloser                     { return nil }
func (r *jsonMockHttpRequest) FormFile(key string) (core.IFile, error)       { return nil, nil }
func (r *jsonMockHttpRequest) GetFiles(key string) ([]core.IFile, error)     { return nil, nil }
func (r *jsonMockHttpRequest) IP() string                                    { return "" }
func (r *jsonMockHttpRequest) Query() core.QueryParams                       { return nil }
func (r *jsonMockHttpRequest) RawRequest() *http.Request                     { return r.req }
func (r *jsonMockHttpRequest) GetMultipartFormValues() *multipart.Form       { return nil }

type jsonMockHttpResponse struct{}

func (r *jsonMockHttpResponse) SetHeader(k, v string)          {}
func (r *jsonMockHttpResponse) Header() http.Header            { return nil }
func (r *jsonMockHttpResponse) Text(b string, c int)           {}
func (r *jsonMockHttpResponse) JSON(v interface{}, c int)      {}
func (r *jsonMockHttpResponse) Bytes(b []byte, c int)          {}
func (r *jsonMockHttpResponse) Redirect(url string, code int)  {}
func (r *jsonMockHttpResponse) RawWriter() http.ResponseWriter { return nil }

type jsonMockSessionScope struct{}

func (s *jsonMockSessionScope) Setup()             {}
func (s *jsonMockSessionScope) Get() core.ISession { return nil }
func (s *jsonMockSessionScope) ClearExpiredFlash() {}

type jsonMockViewScope struct{}

func (v *jsonMockViewScope) Render(tpl string, data map[string]any) {
}

type jsonMockCookieManager struct{}

func (c *jsonMockCookieManager) SetCookie(cookie *http.Cookie)     {}
func (c *jsonMockCookieManager) GetCookie(key string) *http.Cookie { return nil }
func (c *jsonMockCookieManager) GetCookies() []*http.Cookie        { return nil }
