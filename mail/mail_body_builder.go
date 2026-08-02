package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
)

type boundaryContent interface {
	FormatEmail() (*bytes.Buffer, error)
}

type attachmentContent struct {
	ContentID string
	FileName  string
	Content   []byte
}

// format writes one MIME part for an attachment under the given content disposition.
//
// The file name and content id are values an application usually takes from an upload, so
// they are attacker-influenced and are checked for line breaks for the same reason the
// subject is: interpolated into "Content-Disposition: attachment; filename=%s", a newline in
// a file name ends that header and starts one of the attacker's own inside the part.
//
// The parameters go through mime.FormatMediaType rather than straight into the format string.
// That is not only about safety: an unquoted name containing a space or a semicolon — "my
// invoice.pdf" is the common case — truncates at the first delimiter, so the recipient saw
// "my" instead of the file, and a non-ASCII name went out as raw UTF-8 that RFC 2231 says has
// to be encoded. FormatMediaType handles quoting and that encoding.
func (thiz attachmentContent) format(disposition string) (*bytes.Buffer, error) {
	if err := assertNoLineBreak("attachment name", thiz.FileName); err != nil {
		return nil, err
	}
	if err := assertNoLineBreak("attachment id", thiz.ContentID); err != nil {
		return nil, err
	}

	contentType, params, err := mime.ParseMediaType(http.DetectContentType(thiz.Content))
	if err != nil {
		return nil, fmt.Errorf("mail: cannot determine the content type of attachment %q: %w", thiz.FileName, err)
	}
	if params == nil {
		params = make(map[string]string)
	}
	params["name"] = thiz.FileName

	formattedContentType := mime.FormatMediaType(contentType, params)
	formattedDisposition := mime.FormatMediaType(disposition, map[string]string{"filename": thiz.FileName})
	// FormatMediaType reports failure by returning an empty string, and it is the file name that
	// can make it fail. Emitting the empty result would silently drop the part's headers.
	if formattedContentType == "" || formattedDisposition == "" {
		return nil, fmt.Errorf("mail: attachment name cannot be written to a header: %q", thiz.FileName)
	}

	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("Content-Type: %s\r\n", formattedContentType))
	buf.WriteString("Content-Transfer-Encoding: base64\r\n")
	buf.WriteString(fmt.Sprintf("Content-Disposition: %s\r\n", formattedDisposition))
	if thiz.ContentID != "" {
		buf.WriteString(fmt.Sprintf("X-Attachment-Id: %s\r\n", thiz.ContentID))
		buf.WriteString(fmt.Sprintf("Content-ID: <%s>\r\n", thiz.ContentID))
	}
	buf.WriteString("\r\n")

	b := make([]byte, base64.StdEncoding.EncodedLen(len(thiz.Content)))
	base64.StdEncoding.Encode(b, thiz.Content)
	buf.Write(b)

	return buf, nil
}

func (thiz attachmentContent) FormatEmail() (*bytes.Buffer, error) {
	return thiz.format("attachment")
}

type inlineContent struct {
	attachmentContent
}

func (thiz inlineContent) FormatEmail() (*bytes.Buffer, error) {
	return thiz.format("inline")
}

type htmlContent struct {
	Content []byte
}

func (thiz htmlContent) FormatEmail() (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")

	mailBodyBuffer := make([]byte, base64.StdEncoding.EncodedLen(len(thiz.Content)))
	base64.StdEncoding.Encode(mailBodyBuffer, thiz.Content)
	buf.Write(mailBodyBuffer)
	buf.WriteString("\r\n")

	return buf, nil
}

type boundary struct {
	Key            string
	ContentType    string
	Content        []boundaryContent
	ChildBoundary  *boundary
	ParentBoundary *boundary
}

func (thiz *boundary) AddContent(content boundaryContent) {
	if thiz.Content == nil {
		thiz.Content = make([]boundaryContent, 0)
	}
	thiz.Content = append(thiz.Content, content)
}

func (thiz *boundary) FormatContent() (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)

	if len(thiz.Content) == 0 {
		return buf, nil
	}

	buf.WriteString(fmt.Sprintf("--%s\r\n", thiz.Key))
	for i, boundaryContent := range thiz.Content {
		formatted, err := boundaryContent.FormatEmail()
		if err != nil {
			return nil, err
		}

		buf.Write(formatted.Bytes())
		buf.WriteString("\r\n")
		if i == len(thiz.Content)-1 {
			buf.WriteString(fmt.Sprintf("--%s--\r\n", thiz.Key))
		} else {
			buf.WriteString(fmt.Sprintf("--%s\r\n", thiz.Key))
		}
	}

	return buf, nil
}

type bodyBuilder struct {
	Boundary *boundary
}

func (thiz *bodyBuilder) CreateBoundary(contentType, key string) *bodyBuilder {
	boundary := &boundary{
		Key:         key,
		ContentType: contentType,
	}

	if thiz.Boundary == nil {
		thiz.Boundary = boundary
	} else {
		b := thiz.Boundary
		for {
			if b.ChildBoundary == nil {
				boundary.ParentBoundary = b
				b.ChildBoundary = boundary
				break
			}
			b = thiz.Boundary.ChildBoundary
		}
	}
	return thiz
}

func (thiz *bodyBuilder) AddToBoundary(key string, content boundaryContent) *bodyBuilder {
	b := thiz.Boundary
	var foundBoundary *boundary
	for {
		if b == nil {
			break
		}

		if b.Key != key {
			b = b.ChildBoundary
			continue
		}

		foundBoundary = b
		break
	}

	if foundBoundary == nil {
		return thiz
	}

	if foundBoundary.Content == nil {
		foundBoundary.Content = make([]boundaryContent, 0)
	}

	foundBoundary.Content = append(foundBoundary.Content, content)

	return thiz
}

// Build assembles the MIME part of the message.
//
// It replaces a String() method: formatting a part can now fail — an attachment name carrying
// a line break has to stop the message rather than be written out — and a method named String
// that returns an error is a trap for anyone who assumes fmt.Stringer semantics.
//
// A builder with no boundary at all is a mail with neither a body nor an attachment. That used
// to dereference a nil Boundary here, so a caller who left the body empty crashed the request
// instead of sending a headers-only message.
func (thiz *bodyBuilder) Build() ([]byte, error) {
	if thiz.Boundary == nil {
		return nil, nil
	}

	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("Content-Type: %s; boundary=\"%s\"\r\n\r\n", thiz.Boundary.ContentType, thiz.Boundary.Key))

	parentBoundary := thiz.Boundary
	boundaryIter := thiz.Boundary.ChildBoundary
	for {
		if boundaryIter == nil {
			break
		}

		buf.WriteString(fmt.Sprintf("--%s\r\n", parentBoundary.Key))
		buf.WriteString(fmt.Sprintf("Content-Type: %s; boundary=\"%s\"\r\n\r\n", boundaryIter.ContentType, boundaryIter.Key))

		parentBoundary = boundaryIter
		boundaryIter = boundaryIter.ChildBoundary
	}

	boundaryIter = parentBoundary
	for {
		if boundaryIter == nil {
			break
		}

		formatted, err := boundaryIter.FormatContent()
		if err != nil {
			return nil, err
		}

		buf.Write(formatted.Bytes())
		boundaryIter = boundaryIter.ParentBoundary
	}

	return buf.Bytes(), nil
}
