package util

import (
	"golang.org/x/crypto/bcrypt"
	"regexp"
	"strings"
)

func HashWithSalt(rawText string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(rawText), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CompareSaltedHash(hashedPassword, rawPassword string) bool {
	res := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(rawPassword))
	if res != nil {
		return false
	}
	return true
}

func FindValueInTagValues(searchValue, values, separator string) (string, bool) {
	searchValueRegExp := regexp.MustCompile(searchValue)
	splitValues := strings.Split(values, separator)
	for _, value := range splitValues {
		if searchValueRegExp.Match([]byte(value)) {
			return value, true
		}
	}
	return "", false
}
