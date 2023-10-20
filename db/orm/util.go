package orm

import (
	"gorgany/app/core"
	"reflect"
	"strings"
)

func GetValueInTag(tag reflect.StructTag, param string) string {
	gormTag := tag.Get(core.GorganyORMTag)
	splitGormTag := strings.Split(gormTag, ",")
	for _, value := range splitGormTag {
		if strings.Contains(value, param) {
			paramValue := strings.Split(value, "=")
			if len(paramValue) == 1 {
				return ""
			}
			return paramValue[1]
		}
	}
	return ""
}

func IsParamInTagExists(tag reflect.StructTag, param string) bool {
	gormTag := tag.Get(core.GorganyORMTag)
	splitGormTag := strings.Split(gormTag, ",")
	for _, value := range splitGormTag {
		if strings.Contains(value, param) {
			return true
		}
	}
	return false
}
