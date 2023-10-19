package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"gorgany/proxy"
	"mime/multipart"
	"net/http"
	"net/smtp"
	"os"
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

func (thiz MailService) Send(mail proxy.IMail) error {
	body, err := thiz.buildBody(mail)
	if err != nil {
		return err
	}

	return smtp.SendMail(thiz.buildSmtpAddress(), thiz.buildAuth(), thiz.sender, mail.GetRecipients(), body)
}

func (thiz MailService) buildBody(mail proxy.IMail) ([]byte, error) {
	buf := new(bytes.Buffer)

	buf.WriteString(fmt.Sprintf("Subject: %s\n", mail.GetSubject()))
	buf.WriteString("MIME-version: 1.0;\n")
	writer := multipart.NewWriter(buf)
	boundary := writer.Boundary()

	if len(mail.GetAttachments()) == 0 {
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\";\n\n")
	} else {
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\n", boundary))
		buf.WriteString(fmt.Sprintf("--%s\n", boundary))
	}

	mailBody, err := mail.GetBody()
	if err != nil {
		return nil, err
	}

	if mailBody != nil && len(mail.GetAttachments()) > 0 {
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\";\n\n")
	}
	buf.Write(mailBody)

	for _, attachment := range mail.GetAttachments() {
		buf.WriteString(fmt.Sprintf("\n\n--%s\n", boundary))
		buf.WriteString(fmt.Sprintf("Content-Type: %s\n", http.DetectContentType(attachment.GetContent())))
		buf.WriteString("Content-Transfer-Encoding: base64\n")
		buf.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=%s\n", attachment.GetName()))

		b := make([]byte, base64.StdEncoding.EncodedLen(len(attachment.GetContent())))
		base64.StdEncoding.Encode(b, attachment.GetContent())
		buf.Write(b)
		buf.WriteString(fmt.Sprintf("\n--%s", boundary))
	}

	buf.WriteString("--")

	return buf.Bytes(), nil
}

func (thiz MailService) buildSmtpAddress() string {
	return thiz.host + ":" + thiz.port
}

func (thiz MailService) buildAuth() smtp.Auth {
	return smtp.PlainAuth("", thiz.username, thiz.password, thiz.host)
}
