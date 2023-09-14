package auth

import (
	"fmt"
	"gorgany"
	"gorgany/db"
	"gorgany/model"
	"gorgany/proxy"
	"gorgany/util"
	"gorm.io/gorm/schema"
	"reflect"
)

type DynamicAccessService struct {
}

type AccessFilterCondition struct {
	Field string
	Value string
}

func (thiz DynamicAccessService) ResolveFilterAccessCondition(domain any, user proxy.Authenticable, actionType gorgany.DynamicAccessActionType) (*AccessFilterCondition, bool) {
	var reflectedCurrentUserValue reflect.Value
	reflectedDomainType := reflect.TypeOf(domain)

	domainName := reflectedDomainType.Name()

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	db.Builder(proxy.GormPostgresQL).FromModel(model.DynamicAccess{}).Where("domain_name", "=", domainName).List(&dynamicAccesses)

	if len(dynamicAccesses) > 0 {
		reflectedCurrentUserValue = reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	}

	var fieldNamer schema.Namer = schema.NamingStrategy{}
	for _, dynamicAccess := range dynamicAccesses {
		tableName := fieldNamer.TableName(dynamicAccess.DomainName)
		columnName := fieldNamer.ColumnName(tableName, dynamicAccess.DomainProperty)
		reflectedUserProperty := reflectedCurrentUserValue.Elem().FieldByName(dynamicAccess.UserProperty)
		currentUserPropertyValue := fmt.Sprintf("%v", reflectedUserProperty.Interface())
		if currentUserPropertyValue != dynamicAccess.UserPropertyValue {
			continue
		}
		if dynamicAccess.Constraint == gorgany.Edit {
			if dynamicAccess.DomainProperty == "" {
				return nil, true
			}
			return &AccessFilterCondition{Field: columnName, Value: dynamicAccess.DomainPropertyValue}, true
		} else if dynamicAccess.Constraint == gorgany.Show {
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

func (thiz DynamicAccessService) IsAbleToAction(record model.DomainExtension, user proxy.Authenticable, action gorgany.DynamicAccessActionType) bool {
	var reflectedCurrentUserValue reflect.Value
	reflectedDomainType := reflect.TypeOf(record).Elem()

	domainName := reflectedDomainType.Name()

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	db.Builder(proxy.GormPostgresQL).FromModel(model.DynamicAccess{}).Where("domain_name", "=", domainName).List(&dynamicAccesses)

	if len(dynamicAccesses) > 0 {
		reflectedCurrentUserValue = reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	}

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

func (thiz DynamicAccessService) ResolveAccessForRecord(record model.DomainExtension, user proxy.Authenticable) bool {
	reflectedCurrentUserValue := reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	reflectedDomainType := util.IndirectType(reflect.TypeOf(record))

	domainName := reflectedDomainType.Name()

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	db.Builder(proxy.GormPostgresQL).FromModel(model.DynamicAccess{}).Where("domain_name", "=", domainName).List(&dynamicAccesses)

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

func (thiz DynamicAccessService) ResolveActionsForRecord(record model.DomainExtension, user proxy.Authenticable) []gorgany.DynamicAccessActionType {
	var reflectedCurrentUserValue reflect.Value
	reflectedDomainType := util.IndirectType(reflect.TypeOf(record))

	domainName := reflectedDomainType.Name()

	dynamicAccesses := make([]*model.DynamicAccess, 0)
	db.Builder(proxy.GormPostgresQL).FromModel(model.DynamicAccess{}).Where("domain_name", "=", domainName).List(&dynamicAccesses)
	if len(dynamicAccesses) > 0 {
		reflectedCurrentUserValue = reflect.ValueOf(user.(model.DomainExtension).GetDomain())
	}

		accessLevels := make([]gorgany.DynamicAccessActionType, 0)
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
		return []gorgany.DynamicAccessActionType{gorgany.Show, gorgany.Edit, gorgany.Delete}
	}
	if len(accessLevels) > 0 {
		return util.UniqueSlice[gorgany.DynamicAccessActionType](accessLevels)
	}
	return []gorgany.DynamicAccessActionType{}
}

func (thiz DynamicAccessService) resolveAccessLevel(dynamicAccess *model.DynamicAccess) []gorgany.DynamicAccessActionType {
	if dynamicAccess.Constraint == gorgany.Create {
		return []gorgany.DynamicAccessActionType{gorgany.Create}
	}
	if dynamicAccess.Constraint == gorgany.Delete {
		return []gorgany.DynamicAccessActionType{gorgany.Show, gorgany.Edit, gorgany.Delete}
	} else if dynamicAccess.Constraint == gorgany.Edit {
		return []gorgany.DynamicAccessActionType{gorgany.Show, gorgany.Edit}
	}
	return []gorgany.DynamicAccessActionType{gorgany.Show}
}

func (thiz DynamicAccessService) isAccessAllowed(record model.DomainExtension, field string, value string) bool {
	reflectedRecordValue := reflect.ValueOf(record.GetDomain())
	reflectedField := util.IndirectValue(reflectedRecordValue).FieldByName(field)
	val := fmt.Sprintf("%v", reflectedField.Interface())
	if val == value {
		return true
	}

	return false
}
