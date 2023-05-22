package util

import (
	"golang.org/x/crypto/bcrypt"
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
