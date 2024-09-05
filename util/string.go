package util

import (
	"fmt"
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

func Join[T comparable](elements []T, sep string) string {
	output := ""
	for i, el := range elements {
		output += fmt.Sprintf("%v", el)
		if i < len(elements)-1 {
			output += sep
		}
	}
	return output
}

func CamelCase(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})

	if len(words) == 0 {
		return ""
	}

	for i := range words {
		words[i] = strings.ToLower(words[i])
		if i > 0 {
			words[i] = strings.Title(words[i])
		}
	}

	return strings.Join(words, "")
}
