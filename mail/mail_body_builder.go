package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
)

type boundaryContent interface {
	FormatEmail() *bytes.Buffer
}

type attachmentContent struct {
	ContentID string
	FileName  string
	Content   []byte
}

func (thiz attachmentContent) FormatEmail() *bytes.Buffer {
	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("Content-Type: %s; name=%s\n", http.DetectContentType(thiz.Content), thiz.FileName))
	buf.WriteString("Content-Transfer-Encoding: base64\n")
	buf.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=%s\n", thiz.FileName))
	if thiz.ContentID != "" {
		buf.WriteString(fmt.Sprintf("X-Attachment-Id: %s\n", thiz.ContentID))
		buf.WriteString(fmt.Sprintf("Content-ID: <%s>\n", thiz.ContentID))
	}
	buf.WriteString("\n")

	b := make([]byte, base64.StdEncoding.EncodedLen(len(thiz.Content)))
	base64.StdEncoding.Encode(b, thiz.Content)
	buf.Write(b)

	return buf
}

type inlineContent struct {
	attachmentContent
}

func (thiz inlineContent) FormatEmail() *bytes.Buffer {
	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("Content-Type: %s; name=%s\n", http.DetectContentType(thiz.Content), thiz.FileName))
	buf.WriteString("Content-Transfer-Encoding: base64\n")
	buf.WriteString(fmt.Sprintf("Content-Disposition: inline; filename=%s\n", thiz.FileName))
	if thiz.ContentID != "" {
		buf.WriteString(fmt.Sprintf("X-Attachment-Id: %s\n", thiz.ContentID))
		buf.WriteString(fmt.Sprintf("Content-ID: <%s>\n", thiz.ContentID))
	}
	buf.WriteString("\n")

	b := make([]byte, base64.StdEncoding.EncodedLen(len(thiz.Content)))
	base64.StdEncoding.Encode(b, thiz.Content)
	buf.Write(b)

	return buf
}

type htmlContent struct {
	Content []byte
}

func (thiz htmlContent) FormatEmail() *bytes.Buffer {
	buf := new(bytes.Buffer)
	buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\n")
	buf.WriteString("Content-Transfer-Encoding: base64\n\n")

	mailBodyBuffer := make([]byte, base64.StdEncoding.EncodedLen(len(thiz.Content)))
	base64.StdEncoding.Encode(mailBodyBuffer, thiz.Content)
	buf.Write(mailBodyBuffer)
	buf.WriteString("\n")

	return buf
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

func (thiz *boundary) FormatContent() *bytes.Buffer {
	buf := new(bytes.Buffer)

	if len(thiz.Content) == 0 {
		return buf
	}

	buf.WriteString(fmt.Sprintf("--%s\n", thiz.Key))
	for i, boundaryContent := range thiz.Content {
		buf.Write(boundaryContent.FormatEmail().Bytes())
		buf.WriteString("\n")
		if i == len(thiz.Content)-1 {
			buf.WriteString(fmt.Sprintf("--%s--\n", thiz.Key))
		} else {
			buf.WriteString(fmt.Sprintf("--%s\n", thiz.Key))
		}
	}

	return buf
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

func (thiz *bodyBuilder) String() []byte {
	buf := new(bytes.Buffer)
	buf.WriteString(fmt.Sprintf("Content-Type: %s; boundaryIter=\"%s\"\n\n", thiz.Boundary.ContentType, thiz.Boundary.Key))

	parentBoundary := thiz.Boundary
	boundaryIter := thiz.Boundary.ChildBoundary
	for {
		if boundaryIter == nil {
			break
		}

		buf.WriteString(fmt.Sprintf("--%s\n", parentBoundary.Key))
		buf.WriteString(fmt.Sprintf("Content-Type: %s; boundaryIter=\"%s\"\n\n", boundaryIter.ContentType, boundaryIter.Key))

		parentBoundary = boundaryIter
		boundaryIter = boundaryIter.ChildBoundary
	}

	boundaryIter = parentBoundary
	for {
		if boundaryIter == nil {
			break
		}

		buf.Write(boundaryIter.FormatContent().Bytes())
		boundaryIter = boundaryIter.ParentBoundary
	}

	return buf.Bytes()
}
