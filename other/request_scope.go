package http

import (
	"bytes"
	"fmt"
	"github.com/gorganyio/gorgany/decoder"
	"github.com/spf13/viper"
	"io"
	"mime/multipart"
	"net/http"
	url2 "net/url"
)

// HTTPRequestScope handles parsing of incoming HTTP request data.
type HTTPRequestScope struct {
	Request     *http.Request
	cachedQuery *decoder.QueryParams
	MaxBodySize int64 // 0 means no limit
}

// NewHTTPRequestScope constructs a HTTPRequestScope, reading max body size from config.
func NewHTTPRequestScope(r *http.Request) *HTTPRequestScope {
	maxSize := viper.GetInt64("http.max_body_size")
	return &HTTPRequestScope{Request: r, MaxBodySize: maxSize}
}

// GetRawRequest returns the raw http.Request.
func (rc *HTTPRequestScope) GetRawRequest() *http.Request {
	return rc.Request
}

// GetBody reads the entire request body into memory, respecting MaxBodySize.
func (rc *HTTPRequestScope) GetBody() ([]byte, error) {
	var reader io.Reader = rc.Request.Body
	if rc.MaxBodySize > 0 {
		reader = io.LimitReader(rc.Request.Body, rc.MaxBodySize+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("HTTPRequestScope.GetBody: %w", err)
	}
	if rc.MaxBodySize > 0 && int64(len(body)) > rc.MaxBodySize {
		return nil, fmt.Errorf("HTTPRequestScope.GetBody: body too large, max %d bytes", rc.MaxBodySize)
	}
	// reset body for potential re-reading
	rc.Request.Body = io.NopCloser(bytes.NewBuffer(body))
	return body, nil
}

// StreamBody streams the request body directly to dst without full buffering.
func (rc *HTTPRequestScope) StreamBody(dst io.Writer) error {
	if rc.MaxBodySize > 0 {
		lr := io.LimitReader(rc.Request.Body, rc.MaxBodySize+1)
		written, err := io.Copy(dst, lr)
		if err != nil {
			return fmt.Errorf("HTTPRequestScope.StreamBody: %w", err)
		}
		if written > rc.MaxBodySize {
			return fmt.Errorf("HTTPRequestScope.StreamBody: body too large, max %d bytes", rc.MaxBodySize)
		}
	} else {
		if _, err := io.Copy(dst, rc.Request.Body); err != nil {
			return fmt.Errorf("HTTPRequestScope.StreamBody: %w", err)
		}
	}
	return nil
}

// QueryParam retrieves the first value for a query key.
func (rc *HTTPRequestScope) QueryParam(key string) string {
	rc.parseQuery()
	return rc.cachedQuery.GetString(key)
}

// QueryParams retrieves all values for a query key.
func (rc *HTTPRequestScope) QueryParams(key string) []string {
	rc.parseQuery()
	return rc.cachedQuery.GetArray(key)
}

// RawQuery returns the raw query string.
func (rc *HTTPRequestScope) RawQuery() string {
	return rc.Request.URL.RawQuery
}

// FormFile parses multipart form and returns the file and header. Temporary files are cleaned up.
func (rc *HTTPRequestScope) FormFile(key string) (multipart.File, *multipart.FileHeader, error) {
	if err := rc.Request.ParseMultipartForm(32 << 20); err != nil {
		return nil, nil, err
	}
	defer rc.Request.MultipartForm.RemoveAll()
	return rc.Request.FormFile(key)
}

func (rc *HTTPRequestScope) parseQuery() error {
	if rc.cachedQuery != nil {
		return nil
	}
	values, err := url2.ParseQuery(rc.Request.URL.RawQuery)
	if err != nil {
		return err
	}
	parsed, err := decoder.ParseUrlValues(values)
	if err != nil {
		return err
	}
	rc.cachedQuery = &parsed
	return nil
}
