package model

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

type DynamicAccess struct {
	Id int

	DomainName string `validate:"required"`

	DomainProperty string

	DomainPropertyValue string

	UserProperty string `validate:"required"`

	UserPropertyValue string `validate:"required"`

	Constraint core.DynamicAccessActionType `validate:"required"`

	//Fields []struct { //todo
	//	Id              int
	//	Field           string
	//	AccessRecordsId int
	//}
}
