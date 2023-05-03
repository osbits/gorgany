package service

import (
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"graecoFramework"
	"graecoFramework/db"
	"graecoFramework/model"
	"reflect"
)

type DynamicAccessService struct {
}

type AccessFilterCondition struct {
	Field string
	Value string
}

func (thiz DynamicAccessService) ResolveFilterAccessCondition(domain any, user model.Authenticable, actionType graecoFramework.DynamicAccessActionType) (*AccessFilterCondition, bool) {
	reflectedCurrentUserValue := reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	reflectedDomainType := reflect.TypeOf(domain)

	domainName := reflectedDomainType.Name()

	gormInstance := db.GetWrapper("gorm").GetInstance().(*gorm.DB)

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	gormInstance.Find(&dynamicAccesses, "domain_name = ?", domainName)

	var fieldNamer schema.Namer = schema.NamingStrategy{}
	for _, dynamicAccess := range dynamicAccesses {
		tableName := fieldNamer.TableName(dynamicAccess.DomainName)
		columnName := fieldNamer.ColumnName(tableName, dynamicAccess.DomainProperty)
		reflectedUserProperty := reflectedCurrentUserValue.Elem().FieldByName(dynamicAccess.UserProperty)
		currentUserPropertyValue := fmt.Sprintf("%v", reflectedUserProperty.Interface())
		if currentUserPropertyValue != dynamicAccess.UserPropertyValue {
			continue
		}
		if dynamicAccess.Constraint == graecoFramework.Edit {
			if dynamicAccess.DomainProperty == "" {
				return nil, true
			}
			return &AccessFilterCondition{Field: columnName, Value: dynamicAccess.DomainPropertyValue}, true
		} else if dynamicAccess.Constraint == graecoFramework.Show {
			if dynamicAccess.Constraint == actionType {
				if dynamicAccess.DomainProperty == "" {
					return nil, true
				}
				return &AccessFilterCondition{Field: columnName, Value: dynamicAccess.DomainPropertyValue}, true
			}
		} else {
			//todo
		}
	}

	if len(dynamicAccesses) == 0 {
		return nil, true
	}
	return nil, false
}

func (thiz DynamicAccessService) ResolveAccessForRecord(record model.DomainExtension, user model.Authenticable, fullAccess bool) bool {
	reflectedCurrentUserValue := reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	reflectedDomainType := reflect.TypeOf(record.GetDomain()).Elem()

	domainName := reflectedDomainType.Name()

	gormInstance := db.GetWrapper("gorm").GetInstance().(*gorm.DB)

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	gormInstance.Find(&dynamicAccesses, "domain_name = ?", domainName)

	for _, dynamicAccess := range dynamicAccesses {
		reflectedUserProperty := reflectedCurrentUserValue.Elem().FieldByName(dynamicAccess.UserProperty)
		currentUserPropertyValue := fmt.Sprintf("%v", reflectedUserProperty.Interface())
		if currentUserPropertyValue != dynamicAccess.UserPropertyValue {
			continue
		}
		if fullAccess {
			if dynamicAccess.DomainProperty == "" {
				return true
			}
			return thiz.isAccessAllowed(record, dynamicAccess.DomainProperty, dynamicAccess.DomainPropertyValue)
		} else {
			//todo
		}
	}

	if len(dynamicAccesses) == 0 {
		return true
	}
	return false
}

func (thiz DynamicAccessService) isAccessAllowed(record model.DomainExtension, field string, value string) bool {
	reflectedRecordValue := reflect.ValueOf(record.GetDomain())
	reflectedField := reflectedRecordValue.Elem().FieldByName(field)
	val := fmt.Sprintf("%v", reflectedField.Interface())
	if val == value {
		return true
	}

	return false
}
