package mail

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	netmail "net/mail"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
)

type testMail struct {
	recipients  []string
	cc          []string
	bcc         []string
	subject     string
	body        []byte
	attachments []core.IAttachment
}

func (t testMail) GetRecipients() []string { return t.recipients }
func (t testMail) GetCc() []string         { return t.cc }
func (t testMail) GetBcc() []string        { return t.bcc }
func (t testMail) GetSubject() string      { return t.subject }

func (t testMail) GetBody(context.Context) ([]byte, error) { return t.body, nil }

func (t testMail) GetAttachments() ([]core.IAttachment, error) { return t.attachments, nil }

var _ core.IMail = testMail{}

func testService() MailService {
	return MailService{
		sender:   "app@example.com",
		username: "app@example.com",
		password: "secret",
		host:     "smtp.example.com",
		port:     "587",
	}
}

func plainMail() testMail {
	return testMail{
		recipients: []string{"user@example.com"},
		subject:    "Monthly report",
		body:       []byte("<p>hello</p>"),
	}
}

func TestBuildBodyRejectsCarriageReturnNewlineInSubject(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "Password reset\r\nReply-To: attacker@evil.example\r\nContent-Type: text/html\r\n\r\n<phishing>"

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Reply-To")
	assert.NotContains(t, string(body), "attacker@evil.example")
}

func TestBuildBodyRejectsBareNewlineInSubject(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "Password reset\nReply-To: attacker@evil.example"

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Reply-To")
}

func TestBuildBodyRejectsBareCarriageReturnInSubject(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "Password reset\rReply-To: attacker@evil.example"

	_, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
}

func TestBuildBodyRejectsNewlineInRecipient(t *testing.T) {
	service := testService()
	m := plainMail()
	m.recipients = []string{"user@example.com", "victim@example.com\r\nBcc: attacker@evil.example"}

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Bcc")
	assert.NotContains(t, string(body), "attacker@evil.example")
}

func TestBuildBodyRejectsNewlineInCc(t *testing.T) {
	service := testService()
	m := plainMail()
	m.cc = []string{"boss@example.com\nReply-To: attacker@evil.example"}

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Reply-To")
}

func TestBuildBodyRejectsNewlineInSender(t *testing.T) {
	service := testService()
	service.sender = "app@example.com\r\nReply-To: attacker@evil.example"
	m := plainMail()

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Reply-To")
}

func TestBuildBodyRejectsNewlineInAttachmentFilename(t *testing.T) {
	service := testService()
	m := plainMail()
	m.attachments = []core.IAttachment{
		&Attachment{
			Name:               "invoice.pdf\r\nContent-Type: text/html",
			Content:            []byte("payload"),
			ContentDisposition: core.Attachment,
		},
	}

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "Content-Type: text/html")
}

func TestBuildBodyRejectsNewlineInInlineAttachmentFilename(t *testing.T) {
	service := testService()
	m := plainMail()
	m.attachments = []core.IAttachment{
		&Attachment{
			Id:                 "logo",
			Name:               "logo.png\nX-Injected: yes",
			Content:            []byte("payload"),
			ContentDisposition: core.Inline,
		},
	}

	body, err := service.buildBody(context.Background(), m)

	assert.Error(t, err)
	assert.NotContains(t, string(body), "X-Injected")
}

// Send must refuse the message before it reaches the SMTP conversation, so the failure
// has to be the header-validation error and not a dial error against the fake host.
func TestSendRejectsHeaderInjectionBeforeDialing(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "hi\r\nReply-To: attacker@evil.example"

	err := service.Send(context.Background(), m)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "line break")
}

func TestBuildBodyKeepsPlainAsciiSubjectUnencoded(t *testing.T) {
	service := testService()
	m := plainMail()

	body, err := service.buildBody(context.Background(), m)

	assert.NoError(t, err)
	assert.Contains(t, string(body), "Subject: Monthly report\r\n")
	assert.NotContains(t, string(body), "=?")
}

func TestBuildBodyTerminatesHeadersWithCrlf(t *testing.T) {
	service := testService()
	m := plainMail()
	m.cc = []string{"boss@example.com"}

	body, err := service.buildBody(context.Background(), m)

	assert.NoError(t, err)

	headers, _, found := strings.Cut(string(body), "\r\n\r\n")
	assert.True(t, found, "expected a CRLF-terminated header block")
	for _, line := range strings.Split(headers, "\r\n") {
		assert.NotContains(t, line, "\n", "header line contains a bare newline: %q", line)
	}
}

func TestBuildBodyEncodesNonAsciiSubject(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "Grüße vom Büro"

	body, err := service.buildBody(context.Background(), m)

	assert.NoError(t, err)
	assert.NotContains(t, string(body), "Grüße")
	assert.Contains(t, string(body), "Subject: =?utf-8?q?")

	// Encoding is only correct if a client reading the header back gets what was passed in.
	message, err := netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)

	decoded, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	assert.NoError(t, err)
	assert.Equal(t, "Grüße vom Büro", decoded)
}

// Encoding a subject triples its length for a script outside Latin-1, so a subject that fitted
// comfortably in a line before can no longer be written as one. RFC 5322 caps a line at 998
// octets and RFC 5321 lets a server refuse a longer one, so an unfolded encoded subject is a
// message that may not be delivered at all.
func TestBuildBodyFoldsLongEncodedSubject(t *testing.T) {
	service := testService()
	m := plainMail()
	// 135 Cyrillic characters: 270 octets raw, a thousand once Q-encoded.
	m.subject = strings.Repeat("я", 135)

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)

	for _, line := range strings.Split(string(body), "\r\n") {
		assert.LessOrEqual(t, len(line), 998, "line exceeds the RFC 5322 limit: %d octets", len(line))
	}

	// Folding is only correct if unfolding and decoding gives back what was passed in.
	message, err := netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)

	decoded, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	assert.NoError(t, err)
	assert.Equal(t, m.subject, decoded)
}

// Encoding a display name inflates it the same way a subject inflates, so a recipient list that
// was well inside the line limit as raw UTF-8 can cross it once every name is an encoded word.
// Eight ordinary Cyrillic names are enough.
func TestBuildBodyFoldsLongRecipientList(t *testing.T) {
	service := testService()
	m := plainMail()
	m.recipients = nil
	for i := 0; i < 8; i++ {
		name := strings.Repeat("я", 25)
		m.recipients = append(m.recipients, fmt.Sprintf("%s <user%d@example.com>", name, i))
	}

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)

	for _, line := range strings.Split(string(body), "\r\n") {
		assert.LessOrEqual(t, len(line), 998, "line exceeds the RFC 5322 limit: %d octets", len(line))
	}

	// Folding may not cost the recipients: all eight must survive unfolding and decoding.
	message, err := netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)

	addresses, err := message.Header.AddressList("To")
	assert.NoError(t, err)
	assert.Len(t, addresses, 8)
	for i, address := range addresses {
		assert.Equal(t, strings.Repeat("я", 25), address.Name)
		assert.Equal(t, fmt.Sprintf("user%d@example.com", i), address.Address)
	}
}

// A folded header continues with whitespace, so an injected line must stay distinguishable from
// a fold: nothing the mailer emits may start a new line that could be read as its own header.
func TestBuildBodyFoldsOnlyWithLeadingWhitespace(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = strings.Repeat("я", 400)

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)

	headers, _, _ := strings.Cut(string(body), "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n")[1:] {
		isFold := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
		isHeader := strings.Contains(line, ": ")
		assert.True(t, isFold || isHeader, "line is neither a fold nor a header: %q", line)
	}
}

// An ASCII subject holding characters that are special inside an encoded word must not be
// dragged into one: it worked raw before and it still has to come out raw.
func TestBuildBodyKeepsAsciiSubjectWithSpecialCharactersUnencoded(t *testing.T) {
	service := testService()
	m := plainMail()
	m.subject = "Did you forget your password? (ref = 91_4)"

	body, err := service.buildBody(context.Background(), m)

	assert.NoError(t, err)
	assert.Contains(t, string(body), "Subject: Did you forget your password? (ref = 91_4)\r\n")
}

func TestBuildBodyEncodesNonAsciiRecipientDisplayName(t *testing.T) {
	service := testService()
	m := plainMail()
	m.recipients = []string{"Jörg Müller <joerg@example.com>"}

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)
	assert.NotContains(t, string(body), "Jörg")

	message, err := netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)

	addresses, err := message.Header.AddressList("To")
	assert.NoError(t, err)
	assert.Len(t, addresses, 1)
	assert.Equal(t, "Jörg Müller", addresses[0].Name)
	assert.Equal(t, "joerg@example.com", addresses[0].Address)
}

// net/mail refuses an unquoted display name containing a comma, so such an address takes the
// pass-through branch and is neither quoted nor rewritten. This pins that down: the header still
// reads as two addresses, and the line-break check is the only guarantee the branch carries.
func TestBuildBodyPassesThroughAddressItCannotParse(t *testing.T) {
	service := testService()
	m := plainMail()
	m.recipients = []string{"Doe, John <john@example.com>"}

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)
	assert.Contains(t, string(body), "To: Doe, John <john@example.com>\r\n")

	message, err := netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)

	_, err = message.Header.AddressList("To")
	assert.Error(t, err, "an unquoted comma in a display name is still not a valid address list")
}

// An unquoted file name truncates at the first space, so the recipient used to be offered a
// file called "Q3" instead of the attachment.
func TestBuildBodyQuotesAttachmentFilenameContainingSpaces(t *testing.T) {
	service := testService()
	m := plainMail()
	m.attachments = []core.IAttachment{
		&Attachment{
			Name:               "Q3 invoice.txt",
			Content:            []byte("hello"),
			ContentDisposition: core.Attachment,
		},
	}

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)
	assert.Contains(t, string(body), `Content-Disposition: attachment; filename="Q3 invoice.txt"`)
}

// A mail with neither a body nor an attachment used to dereference a nil boundary.
func TestBuildBodyWithoutBodyOrAttachments(t *testing.T) {
	service := testService()
	m := plainMail()
	m.body = nil

	body, err := service.buildBody(context.Background(), m)
	assert.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(body), "\r\n\r\n"), "expected a terminated header block, got %q", string(body))

	_, err = netmail.ReadMessage(bytes.NewReader(body))
	assert.NoError(t, err)
}
