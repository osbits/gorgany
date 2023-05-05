package service

import (
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"graecoFramework"
	"graecoFramework/db"
	"graecoFramework/model"
	"graecoFramework/util"
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
		}
	}

	if len(dynamicAccesses) == 0 {
		return nil, true
	}
	return nil, false
}

func (thiz DynamicAccessService) IsAbleToAction(record model.DomainExtension, user model.Authenticable, action graecoFramework.DynamicAccessActionType) bool {
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
		if action == dynamicAccess.Constraint {
			return true
		}
	}

	if len(dynamicAccesses) == 0 {
		return true
	}
	return false
}

func (thiz DynamicAccessService) ResolveAccessForRecord(record model.DomainExtension, user model.Authenticable) bool {
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
		if dynamicAccess.DomainProperty == "" {
			return true
		}
		return thiz.isAccessAllowed(record, dynamicAccess.DomainProperty, dynamicAccess.DomainPropertyValue)
	}

	if len(dynamicAccesses) == 0 {
		return true
	}
	return false
}

func (thiz DynamicAccessService) ResolveActionsForRecord(record model.DomainExtension, user model.Authenticable) []graecoFramework.DynamicAccessActionType {
	reflectedCurrentUserValue := reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	reflectedDomainType := reflect.TypeOf(record.GetDomain()).Elem()

	domainName := reflectedDomainType.Name()

	gormInstance := db.GetWrapper("gorm").GetInstance().(*gorm.DB)

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	gormInstance.Find(&dynamicAccesses, "domain_name = ?", domainName)

	accessLevels := make([]graecoFramework.DynamicAccessActionType, 0)
	for _, dynamicAccess := range dynamicAccesses {
		reflectedUserProperty := reflectedCurrentUserValue.Elem().FieldByName(dynamicAccess.UserProperty)
		currentUserPropertyValue := fmt.Sprintf("%v", reflectedUserProperty.Interface())
		if currentUserPropertyValue != dynamicAccess.UserPropertyValue {
			continue
		}

		if dynamicAccess.DomainProperty == "" {
			accessLevels = append(accessLevels, thiz.resolveAccessLevel(dynamicAccess)...)
		} else if thiz.isAccessAllowed(record, dynamicAccess.DomainProperty, dynamicAccess.DomainPropertyValue) {
			accessLevels = append(accessLevels, thiz.resolveAccessLevel(dynamicAccess)...)
		}
	}

	if len(dynamicAccesses) == 0 {
		return []graecoFramework.DynamicAccessActionType{graecoFramework.Show, graecoFramework.Edit, graecoFramework.Delete}
	}
	if len(accessLevels) > 0 {
		return util.UniqueSlice[graecoFramework.DynamicAccessActionType](accessLevels)
	}
	return []graecoFramework.DynamicAccessActionType{}
}

func (thiz DynamicAccessService) resolveAccessLevel(dynamicAccess *model.DynamicAccess) []graecoFramework.DynamicAccessActionType {
	if dynamicAccess.Constraint == graecoFramework.Create {
		return []graecoFramework.DynamicAccessActionType{graecoFramework.Create}
	}
	if dynamicAccess.Constraint == graecoFramework.Delete {
		return []graecoFramework.DynamicAccessActionType{graecoFramework.Show, graecoFramework.Edit, graecoFramework.Delete}
	} else if dynamicAccess.Constraint == graecoFramework.Edit {
		return []graecoFramework.DynamicAccessActionType{graecoFramework.Show, graecoFramework.Edit}
	}
	return []graecoFramework.DynamicAccessActionType{graecoFramework.Show}
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
