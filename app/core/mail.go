package core

import "context"

type IMail interface {
	GetRecipients() []string
	GetCc() []string
	GetBcc() []string
	GetBody(ctx context.Context) ([]byte, error)
	GetSubject() string
	GetAttachments() ([]IAttachment, error)
}

type IAttachment interface {
	GetId() string
	GetName() string
	GetContent() []byte
	GetContentDisposition() ContentDisposition
}
