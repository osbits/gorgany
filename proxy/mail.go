package proxy

type IMail interface {
	GetRecipients() []string
	GetBody() ([]byte, error)
	GetSubject() string
	GetAttachments() []IAttachment
}

type IAttachment interface {
	GetName() string
	GetContent() []byte
}
