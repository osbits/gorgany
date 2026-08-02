package mail

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"mime"
	netmail "net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

type Attachment struct {
	Id                 string
	Name               string
	Content            []byte
	ContentDisposition core.ContentDisposition
}

func (thiz *Attachment) GetId() string {
	return thiz.Id
}

func (thiz *Attachment) GetName() string {
	return thiz.Name
}

func (thiz *Attachment) GetContent() []byte {
	return thiz.Content
}

func (thiz *Attachment) GetContentDisposition() core.ContentDisposition {
	if thiz.ContentDisposition == "" {
		return core.Attachment
	}

	return thiz.ContentDisposition
}

func NewMailService(from ...string) *MailService {
	sender := ""
	if len(from) == 0 {
		sender = os.Getenv("SMTP_SENDER_EMAIL")
	} else {
		sender = from[0]
	}

	username := os.Getenv("SMTP_USERNAME")
	password := os.Getenv("SMTP_PASSWORD")
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")

	return &MailService{password: password, username: username, host: host, sender: sender, port: port}
}

type MailService struct {
	password string
	username string
	host     string
	sender   string
	port     string
}

// assertNoLineBreak refuses a value that cannot be a single header value.
//
// The message is assembled by writing "Name: value" lines, so a CR or LF anywhere in a
// subject, an address or an attachment name does not produce a header with a newline in it —
// it produces extra headers, and after a blank line, an entire replacement body. Nothing
// downstream catches this: smtp.SendMail's validateLine guards only the envelope sender and
// recipients, and textproto.DotWriter guards only the end of DATA. So the attacker cannot add
// envelope recipients or speak SMTP, but a subject of
// "hi\r\nReply-To: attacker@evil\r\nContent-Type: text/html\r\n\r\n<phishing>" sends a message
// they authored in full, from the application's authenticated SMTP identity, with the
// application's own SPF and DKIM vouching for it.
//
// The vector is ordinary application code: a contact form, a "new message from <name>"
// notification, a password reset that greets the user by name. That is why the check lives
// here instead of being every caller's duty.
//
// It rejects instead of stripping. A stripped newline still delivers a plausible-looking
// message, so the operator never learns that someone is probing the mailer; a returned error
// reaches the app's error handling and the log.
func assertNoLineBreak(field, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("mail: %s contains a line break: %q", field, value)
	}

	return nil
}

// encodeHeaderText renders a header's text value the way RFC 2047 requires.
//
// A subject used to be interpolated raw, so a non-ASCII one — the normal case for most of the
// world — went out as bare UTF-8 bytes in a header that is only allowed to carry them to a hop
// that negotiated SMTPUTF8. Whether the recipient saw the subject or saw mojibake was up to
// the relay.
//
// Encoding here is safe for the ASCII case that already worked: mime.QEncoding.Encode returns
// its input untouched unless the value holds a byte outside printable ASCII, so an everyday
// English subject is byte-for-byte what it was before, and nothing that reads the sent message
// has to learn to decode.
//
// The result is folded because encoding is not length-neutral: a Cyrillic or CJK subject grows
// roughly threefold, so 135 characters — 270 octets, an unremarkable subject — come out as a
// single header line of a thousand. RFC 5322 caps a line at 998 octets and RFC 5321 lets a
// server refuse a longer one, which would turn "the subject is now encoded correctly" into "the
// message is not delivered", and only for the alphabets the encoding was added to serve.
// mime.QEncoding splits its output into 75-character encoded words but joins them with a bare
// space where RFC 2047 allows CRLF and a space, so the fold is put back here. A space can only
// be a word separator in that output — an encoded word writes a real space as "_" — so replacing
// every one of them is safe, and whitespace between adjacent encoded words is dropped when they
// are decoded. A value that needed no encoding is returned untouched and unfolded.
//
// One thing this deliberately does not do is neutralise a value a user typed to *look* like an
// encoded word, which a receiving client will decode. That is a display concern rather than a
// header-structure one, and the stdlib offers no way to force an encode of ASCII input; an app
// that cares should reject such input before it reaches the mailer.
func encodeHeaderText(value string) string {
	encoded := mime.QEncoding.Encode("utf-8", value)
	if encoded == value {
		return encoded
	}

	return strings.ReplaceAll(encoded, " ", "\r\n ")
}

// formatAddress normalises one address for an address header.
//
// A parsed address is re-emitted through net/mail so that a display name is quoted and, when it
// is not ASCII, encoded — raw UTF-8 in a display name is only legal to a hop that negotiated
// SMTPUTF8. A bare address is written back bare rather than as "<addr>", because that is the
// shape this header has always had and the angle brackets buy nothing.
//
// An address net/mail cannot parse is passed through untouched. The header is cosmetic —
// delivery follows the envelope — and rewriting a value we failed to understand would be a
// guess; it has already cleared the line-break check, which is the part that carries risk.
//
// Note what that fallback covers, because it is wider than it looks: a display name holding a
// comma but no quotes, "Doe, John <john@example.com>", is not a single address as far as
// net/mail is concerned, so it is passed through rather than repaired and still reads to the
// recipient as two addresses. Nothing here promotes such a value into a quoted name; a caller
// that builds an address from a user-supplied name has to quote it.
func formatAddress(address string) string {
	parsed, err := netmail.ParseAddress(address)
	if err != nil {
		return address
	}

	if parsed.Name == "" {
		return parsed.Address
	}

	return parsed.String()
}

// formatAddressList renders one address header's worth of addresses.
//
// The list is folded after each comma rather than joined onto one line, for the same reason the
// subject is folded: encoding a display name inflates it about threefold, so eight recipients
// with ordinary Cyrillic names — well under the line limit while they were raw UTF-8 — became a
// single To line of some 1700 octets, past the 998 RFC 5322 allows and past the point where a
// receiving server may refuse the line outright. Folding after the comma is the form RFC 5322
// gives for exactly this, an unfolding reader rejoins the addresses, and the fold caps the line
// at one address instead of all of them.
//
// The fold is written here and not by substituting whitespace afterwards because a formatted
// address legitimately contains spaces — between a quoted name and its angle-addr, and inside
// the name itself — and only the separators may be broken.
func formatAddressList(field string, addresses []string) (string, error) {
	formatted := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if err := assertNoLineBreak(field, address); err != nil {
			return "", err
		}

		formatted = append(formatted, formatAddress(address))
	}

	return strings.Join(formatted, ",\r\n "), nil
}

func (thiz MailService) Send(ctx context.Context, mail core.IMail) error {
	body, err := thiz.buildBody(ctx, mail)
	if err != nil {
		return err
	}

	// Bcc never reaches a header, so it is not an injection vector, but smtp.SendMail rejects a
	// CR or LF in it with a message that names neither the field nor the mailer. Checking it here
	// costs nothing and gives the operator the same error shape as every other address.
	for _, bcc := range mail.GetBcc() {
		if err := assertNoLineBreak("bcc recipient", bcc); err != nil {
			return err
		}
	}

	recipients := util.MergeSlice(mail.GetRecipients(), mail.GetCc(), mail.GetBcc())

	return smtp.SendMail(thiz.buildSmtpAddress(), thiz.buildAuth(), thiz.sender, recipients, body)
}

func (thiz MailService) buildBody(ctx context.Context, mail core.IMail) ([]byte, error) {
	if err := assertNoLineBreak("sender", thiz.sender); err != nil {
		return nil, err
	}

	recipients, err := formatAddressList("recipient", mail.GetRecipients())
	if err != nil {
		return nil, err
	}

	cc, err := formatAddressList("cc recipient", mail.GetCc())
	if err != nil {
		return nil, err
	}

	subject := mail.GetSubject()
	if err := assertNoLineBreak("subject", subject); err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)

	buf.WriteString(fmt.Sprintf("From: %s\r\n", formatAddress(thiz.sender)))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", recipients))

	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", encodeHeaderText(subject)))

	if cc != "" {
		buf.WriteString(fmt.Sprintf("Cc: %s\r\n", cc))
	}

	buf.WriteString("MIME-version: 1.0\r\n")

	bodyBuilder := bodyBuilder{}

	allAttachments, err := mail.GetAttachments()
	if err != nil {
		return nil, err
	}

	attachments := util.FindAll(allAttachments, func(el core.IAttachment) bool {
		return el.GetContentDisposition() == core.Attachment
	})
	inlines := util.FindAll(allAttachments, func(el core.IAttachment) bool {
		return el.GetContentDisposition() == core.Inline
	})

	if len(attachments) > 0 {
		md5Sum := md5.Sum([]byte(fmt.Sprintf("attachment_boundary_%d", time.Now().Unix())))
		key := hex.EncodeToString(md5Sum[:])
		bodyBuilder.CreateBoundary("multipart/mixed", key)

		for _, attachment := range attachments {
			bodyBuilder.AddToBoundary(key, attachmentContent{
				ContentID: attachment.GetId(),
				FileName:  attachment.GetName(),
				Content:   attachment.GetContent(),
			})
		}
	}
	if len(inlines) > 0 {
		md5Sum := md5.Sum([]byte(fmt.Sprintf("inline_boundary_%d", time.Now().Unix())))
		key := hex.EncodeToString(md5Sum[:])
		bodyBuilder.CreateBoundary("multipart/related", key)

		for _, attachment := range inlines {
			bodyBuilder.AddToBoundary(key, inlineContent{
				attachmentContent: attachmentContent{
					ContentID: attachment.GetId(),
					FileName:  attachment.GetName(),
					Content:   attachment.GetContent(),
				},
			})
		}
	}

	mailBody, err := mail.GetBody(ctx)
	if err != nil {
		return nil, err
	}
	if mailBody != nil {
		md5Sum := md5.Sum([]byte(fmt.Sprintf("html_boundary_%d", time.Now().Unix())))
		key := hex.EncodeToString(md5Sum[:])
		bodyBuilder.CreateBoundary("multipart/alternative", key)

		bodyBuilder.AddToBoundary(key, htmlContent{Content: mailBody})

	}

	mimeBody, err := bodyBuilder.Build()
	if err != nil {
		return nil, err
	}

	// A mail with neither a body nor an attachment produces no MIME part, and the blank line that
	// ends the header block came out of the first part's preamble. Without one, MIME-version is
	// the last thing on the wire and the next hop sees a message that was cut off mid-header.
	if len(mimeBody) == 0 {
		buf.WriteString("\r\n")
	}

	buf.Write(mimeBody)

	return buf.Bytes(), nil
}

func (thiz MailService) buildSmtpAddress() string {
	return thiz.host + ":" + thiz.port
}

func (thiz MailService) buildAuth() smtp.Auth {
	return smtp.PlainAuth("", thiz.username, thiz.password, thiz.host)
}
