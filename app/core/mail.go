package core

type IMail interface {
	GetRecipients() []string
	GetBody() ([]byte, error)
	GetSubject() string
	GetAttachments() ([]IAttachment, error)
}

type IAttachment interface {
	GetName() string
	GetContent() []byte
}
