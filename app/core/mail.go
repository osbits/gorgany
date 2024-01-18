package core

import "context"

type IMail interface {
	GetRecipients() []string
	GetBody(ctx context.Context) ([]byte, error)
	GetSubject() string
	GetAttachments() ([]IAttachment, error)
}

type IAttachment interface {
	GetName() string
	GetContent() []byte
}
