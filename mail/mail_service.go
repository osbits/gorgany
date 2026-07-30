package mail

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
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

	buf.WriteString("MIME-version: 1.0\n")

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

	buf.Write(bodyBuilder.String())

	return buf.Bytes(), nil
}

func (thiz MailService) buildSmtpAddress() string {
	return thiz.host + ":" + thiz.port
}

func (thiz MailService) buildAuth() smtp.Auth {
	return smtp.PlainAuth("", thiz.username, thiz.password, thiz.host)
}
