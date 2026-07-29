package http

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/decoder"
	error2 "github.com/osbits/gorgany/err"
)

// QueryTestStruct represents a simple struct for testing basic query parsing
type QueryTestStruct struct {
	Name      string   `scheme:"name"`
	Age       int      `scheme:"age"`
	Tags      []string `scheme:"tags"`
	Optional  *string  `scheme:"optional"`
	DataValue string   `scheme:"data[value]"`
}

// QueryTestNested represents a nested struct for testing complex query parsing
type QueryTestNested struct {
	Value string `scheme:"value"`
	Count int    `scheme:"count"`
}

type QueryTestInterfaceStruct struct {
	Data   map[string]any `scheme:"data"`
	Values []any          `scheme:"values"`
	Rows   []any          `scheme:"rows"`
}

// QueryTestDomainStruct represents a domain struct for testing query domain parsing
type QueryTestDomainStruct struct {
	ID   int    `scheme:"id" db:"id"`
	Name string `scheme:"name" db:"name"`
}

func (t *QueryTestDomainStruct) GetLoaded() bool {
	return t.ID > 0
}

func (t *QueryTestDomainStruct) GetID() int {
	return t.ID
}

// QueryTestBindStruct represents a struct with bind methods for query parsing
type QueryTestBindStruct struct {
	CustomField string `scheme:"custom_field"`
}

func (t *QueryTestBindStruct) BindCustomField(value string) error {
	if value == "invalid" {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: "custom_field", Err: "invalid value"},
		}
	}
	t.CustomField = value
	return nil
}

func createQueryRequest(t *testing.T, values url.Values) *http.Request {
	req, err := http.NewRequest("GET", "/?"+values.Encode(), nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	return req
}

func TestQueryParser_Parse_Basic(t *testing.T) {
	tests := []struct {
		name    string
		values  url.Values
		want    QueryTestStruct
		wantErr bool
	}{
		{
			name: "valid basic struct",
			values: url.Values{
				"name":              {"John"},
				"age":               {"30"},
				"tags":              {"tag1", "tag2"},
				"optional":          {"optional value"},
				"data[value]":       {"test"},
				"data[value_slice]": {"42", "51"},
			},
			want: QueryTestStruct{
				Name:      "John",
				Age:       30,
				Tags:      []string{"tag1", "tag2"},
				Optional:  stringPtr("optional value"),
				DataValue: "test",
			},
			wantErr: false,
		},
		{
			name: "invalid age type",
			values: url.Values{
				"name": {"John"},
				"age":  {"invalid"},
			},
			wantErr: true,
		},
		{
			name: "empty values",
			values: url.Values{
				"name": {""},
				"age":  {"0"},
			},
			want: QueryTestStruct{
				Name: "",
				Age:  0,
			},
			wantErr: false,
		},
		{
			name:    "empty query",
			values:  url.Values{},
			want:    QueryTestStruct{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createQueryRequest(t, tt.values)
			parser := &QueryParser{message: &queryMockHttpMessage{req: req}}

			var got QueryTestStruct
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
			if got.DataValue != tt.want.DataValue {
				t.Errorf("Data.Value = %v, want %v", got.DataValue, tt.want.DataValue)
			}
		})
	}
}

func TestQueryParser_Parse_InterfaceValues(t *testing.T) {
	values := url.Values{
		"values":     {"181750", "false"},
		"rows[0][a]": {"1"},
		"rows[1][b]": {"false"},
	}
	req := createQueryRequest(t, values)
	parser := &QueryParser{message: &queryMockHttpMessage{req: req}}

	var got QueryTestInterfaceStruct
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

func TestQueryParser_Parse_BindMethod(t *testing.T) {
	tests := []struct {
		name    string
		values  url.Values
		want    QueryTestBindStruct
		wantErr bool
	}{
		{
			name: "valid custom field",
			values: url.Values{
				"custom_field": {"valid value"},
			},
			want: QueryTestBindStruct{
				CustomField: "valid value",
			},
			wantErr: false,
		},
		{
			name: "invalid custom field",
			values: url.Values{
				"custom_field": {"invalid"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createQueryRequest(t, tt.values)
			parser := &QueryParser{message: &queryMockHttpMessage{req: req}}

			var got QueryTestBindStruct
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

// Mock HTTP message for testing
type queryMockHttpMessage struct {
	req *http.Request
}

func (m *queryMockHttpMessage) Request() core.IRequestScope {
	return &queryMockHttpRequest{req: m.req}
}

func (m *queryMockHttpMessage) Response() core.IResponseScope {
	return &queryMockHttpResponse{}
}

func (m *queryMockHttpMessage) Session() core.ISessionScope {
	return &queryMockSessionScope{}
}

func (m *queryMockHttpMessage) View() core.IViewScope {
	return &queryMockViewScope{}
}

func (m *queryMockHttpMessage) Cookie() core.ICookieManager {
	return &queryMockCookieManager{}
}

func (m *queryMockHttpMessage) Context() context.Context {
	return context.Background()
}

func (m *queryMockHttpMessage) Close() error {
	return nil
}

func (m *queryMockHttpMessage) RedirectWithFlash(url string, code int, data map[string]interface{}) {
	// Mock implementation
}

type queryMockHttpRequest struct {
	req *http.Request
}

func (r *queryMockHttpRequest) Body() ([]byte, error) {
	return nil, nil
}

func (r *queryMockHttpRequest) Locale() string                                { return "" }
func (r *queryMockHttpRequest) PathParam(name string) string                  { return "" }
func (r *queryMockHttpRequest) QueryParam(key string) string                  { return "" }
func (r *queryMockHttpRequest) QueryParams(key string) []string               { return nil }
func (r *queryMockHttpRequest) QueryParamsMap(key string) []map[string]string { return nil }
func (r *queryMockHttpRequest) RawQuery() string                              { return r.req.URL.RawQuery }
func (r *queryMockHttpRequest) Header() http.Header                           { return nil }
func (r *queryMockHttpRequest) BodyReader() io.ReadCloser                     { return nil }
func (r *queryMockHttpRequest) FormFile(key string) (core.IFile, error)       { return nil, nil }
func (r *queryMockHttpRequest) GetFiles(key string) ([]core.IFile, error)     { return nil, nil }
func (r *queryMockHttpRequest) IP() string                                    { return "" }
func (r *queryMockHttpRequest) Query() core.QueryParams {
	params, err := decoder.ParseUrlValues(r.req.URL.Query())
	if err != nil {
		return nil
	}
	return params
}
func (r *queryMockHttpRequest) RawRequest() *http.Request               { return r.req }
func (r *queryMockHttpRequest) GetMultipartFormValues() *multipart.Form { return nil }

type queryMockHttpResponse struct{}

func (r *queryMockHttpResponse) SetHeader(k, v string)          {}
func (r *queryMockHttpResponse) Header() http.Header            { return nil }
func (r *queryMockHttpResponse) Text(b string, c int)           {}
func (r *queryMockHttpResponse) JSON(v interface{}, c int)      {}
func (r *queryMockHttpResponse) Bytes(b []byte, c int)          {}
func (r *queryMockHttpResponse) Redirect(url string, code int)  {}
func (r *queryMockHttpResponse) RawWriter() http.ResponseWriter { return nil }

type queryMockSessionScope struct{}

func (s *queryMockSessionScope) Setup()             {}
func (s *queryMockSessionScope) Get() core.ISession { return nil }
func (s *queryMockSessionScope) ClearExpiredFlash() {}

type queryMockViewScope struct{}

func (v *queryMockViewScope) Render(tpl string, data map[string]any) {}

type queryMockCookieManager struct{}

func (c *queryMockCookieManager) SetCookie(cookie *http.Cookie)     {}
func (c *queryMockCookieManager) GetCookie(key string) *http.Cookie { return nil }
func (c *queryMockCookieManager) GetCookies() []*http.Cookie        { return nil }
