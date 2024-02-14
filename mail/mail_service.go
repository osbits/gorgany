package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/util"
	"mime/multipart"
	"net/http"
	"net/smtp"
	"os"
	"strings"
)

type Attachment struct {
	Name    string
	Content []byte
}

func (thiz *Attachment) GetName() string {
	return thiz.Name
}

func (thiz *Attachment) GetContent() []byte {
	return thiz.Content
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

func (thiz MailService) Send(ctx context.Context, mail core.IMail) error {
	body, err := thiz.buildBody(ctx, mail)
	if err != nil {
		return err
	}

	recipients := util.MergeSlice(mail.GetRecipients(), mail.GetCc(), mail.GetBcc())
	return smtp.SendMail(thiz.buildSmtpAddress(), thiz.buildAuth(), thiz.sender, recipients, body)
}

func (thiz MailService) buildBody(ctx context.Context, mail core.IMail) ([]byte, error) {
	buf := new(bytes.Buffer)

	buf.WriteString(fmt.Sprintf("From: %s\n", thiz.sender))
	buf.WriteString(fmt.Sprintf("To: %s\n", strings.Join(mail.GetRecipients(), ", ")))

	buf.WriteString(fmt.Sprintf("Subject: %s\n", mail.GetSubject()))

	if len(mail.GetCc()) > 0 {
		buf.WriteString(fmt.Sprintf("Cc: %s\n", strings.Join(mail.GetCc(), ", ")))
	}
	if len(mail.GetBcc()) > 0 {
		buf.WriteString(fmt.Sprintf("Bcc: %s\n", strings.Join(mail.GetBcc(), ", ")))
	}

	buf.WriteString("MIME-version: 1.0\n")
	writer := multipart.NewWriter(buf)
	boundary := writer.Boundary()

	attachments, err := mail.GetAttachments()
	if err != nil {
		return nil, err
	}

	if len(attachments) == 0 {
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\n\n")
	} else {
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\n\n", boundary))
		buf.WriteString(fmt.Sprintf("--%s\n", boundary))
	}

	mailBody, err := mail.GetBody(ctx)
	if err != nil {
		return nil, err
	}

	if mailBody != nil && len(attachments) > 0 {
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\n")
		buf.WriteString("Content-Transfer-Encoding: base64\n\n")
	}

	mailBodyBuffer := make([]byte, base64.StdEncoding.EncodedLen(len(mailBody)))
	base64.StdEncoding.Encode(mailBodyBuffer, mailBody)
	buf.Write(mailBodyBuffer)

	for _, attachment := range attachments {
		buf.WriteString(fmt.Sprintf("\n\n--%s\n", boundary))
		buf.WriteString(fmt.Sprintf("Content-Type: %s\n", http.DetectContentType(attachment.GetContent())))
		buf.WriteString("Content-Transfer-Encoding: base64\n")
		buf.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=%s\n\n", attachment.GetName()))

		b := make([]byte, base64.StdEncoding.EncodedLen(len(attachment.GetContent())))
		base64.StdEncoding.Encode(b, attachment.GetContent())
		buf.Write(b)
		buf.WriteString(fmt.Sprintf("\n\n--%s", boundary))
	}

	return buf.Bytes(), nil
}

func (thiz MailService) buildSmtpAddress() string {
	return thiz.host + ":" + thiz.port
}

func (thiz MailService) buildAuth() smtp.Auth {
	return smtp.PlainAuth("", thiz.username, thiz.password, thiz.host)
}
