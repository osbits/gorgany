package model

import "gorgany"

type DynamicAccess struct {
	Id int

	DomainName string `validate:"required"`

	DomainProperty string

	DomainPropertyValue string

	UserProperty string `validate:"required"`

	UserPropertyValue string `validate:"required"`

	Constraint gorgany.DynamicAccessActionType `validate:"required"`

	//Fields []struct { //todo
	//	Id              int
	//	Field           string
	//	AccessRecordsId int
	//}
}
