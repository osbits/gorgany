package core

import "context"

// IMail defines the interface for email messages
type IMail interface {
	// GetRecipients returns the list of primary recipients
	GetRecipients() []string
	// GetCc returns the list of CC recipients
	GetCc() []string
	// GetBcc returns the list of BCC recipients
	GetBcc() []string
	// GetBody returns the email body content
	GetBody(ctx context.Context) ([]byte, error)
	// GetSubject returns the email subject
	GetSubject() string
	// GetAttachments returns the list of email attachments
	GetAttachments() ([]IAttachment, error)
}

// IAttachment defines the interface for email attachments
type IAttachment interface {
	// GetId returns the unique identifier of the attachment
	GetId() string
	// GetName returns the name of the attachment
	GetName() string
	// GetContent returns the attachment content
	GetContent() []byte
	// GetContentDisposition returns the content disposition of the attachment
	GetContentDisposition() ContentDisposition
}
