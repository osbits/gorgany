package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

type LocalizedString struct {
	Id   int
	Data LocalizedStringEntries
}

func (thiz LocalizedString) TableName() string {
	return "localized_string"
}

func (thiz LocalizedString) Text(lang string) string {
	localizedMap := thiz.Map()
	return localizedMap[lang]
}

func (thiz LocalizedString) Map() map[string]string {
	return thiz.Data
}

type LocalizedStringEntries map[string]string

func (thiz *LocalizedStringEntries) Scan(value interface{}) error {
	localizedJson, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to cast value:", value))
	}

	if err := json.Unmarshal([]byte(localizedJson), thiz); err != nil {
		return err
	}

	return nil
}

func (thiz LocalizedStringEntries) Value() (driver.Value, error) {
	localizedJson, err := json.Marshal(thiz)
	return string(localizedJson), err
}

type LocalizedStringEntry struct {
	Lang string
	Text string
}

func (thiz *LocalizedStringEntry) Scan(value interface{}) error {
	localizedJson, ok := value.(string)
	if !ok {
		return errors.New(fmt.Sprint("Failed to cast value:", value))
	}

	if err := json.Unmarshal([]byte(localizedJson), thiz); err != nil {
		return err
	}

	return nil
}

func (thiz LocalizedStringEntry) Value() (driver.Value, error) {
	localizedJson, err := json.Marshal(thiz)
	return string(localizedJson), err
}
