package util

import (
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"regexp"
	"strings"
	"unicode"
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
	// First, split by common delimiters
	words := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})

	var allWords []string

	// Then, for each word, split by case changes (uppercase to lowercase)
	for _, word := range words {
		if word == "" {
			continue
		}

		// Split by case change: lowercase to uppercase
		var splitByCase []string
		lastSplit := 0
		runes := []rune(word)

		for i := 1; i < len(runes); i++ {
			// If current character is uppercase and previous is not
			if unicode.IsUpper(runes[i]) && !unicode.IsUpper(runes[i-1]) {
				splitByCase = append(splitByCase, string(runes[lastSplit:i]))
				lastSplit = i
				// If current character is lowercase and previous is uppercase and not the start of a word
			} else if i > 1 && unicode.IsLower(runes[i]) && unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i-2]) {
				splitByCase = append(splitByCase, string(runes[lastSplit:i-1]))
				lastSplit = i - 1
			}
		}

		// Add the remaining part
		if lastSplit < len(runes) {
			splitByCase = append(splitByCase, string(runes[lastSplit:]))
		}

		// If no splits occurred (word has consistent case), use the original word
		if len(splitByCase) == 0 {
			allWords = append(allWords, word)
		} else {
			allWords = append(allWords, splitByCase...)
		}
	}

	if len(allWords) == 0 {
		return ""
	}

	lowerCase := cases.Lower(language.English)
	titleCase := cases.Title(language.English)

	// First word should be entirely lowercase
	allWords[0] = lowerCase.String(allWords[0])

	// Subsequent words should have their first letter capitalized
	for i := 1; i < len(allWords); i++ {
		allWords[i] = titleCase.String(lowerCase.String(allWords[i]))
	}

	return strings.Join(allWords, "")
}

func StudlyCase(s string) string {
	// First, split by common delimiters
	words := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})

	var allWords []string

	// Then, for each word, split by case changes (uppercase to lowercase)
	for _, word := range words {
		if word == "" {
			continue
		}

		// Split by case change: lowercase to uppercase
		var splitByCase []string
		lastSplit := 0
		runes := []rune(word)

		for i := 1; i < len(runes); i++ {
			// If current character is uppercase and previous is not
			if unicode.IsUpper(runes[i]) && !unicode.IsUpper(runes[i-1]) {
				splitByCase = append(splitByCase, string(runes[lastSplit:i]))
				lastSplit = i
				// If current character is lowercase and previous is uppercase and not the start of a word
			} else if i > 1 && unicode.IsLower(runes[i]) && unicode.IsUpper(runes[i-1]) && unicode.IsUpper(runes[i-2]) {
				splitByCase = append(splitByCase, string(runes[lastSplit:i-1]))
				lastSplit = i - 1
			}
		}

		// Add the remaining part
		if lastSplit < len(runes) {
			splitByCase = append(splitByCase, string(runes[lastSplit:]))
		}

		// If no splits occurred (word has consistent case), use the original word
		if len(splitByCase) == 0 {
			allWords = append(allWords, word)
		} else {
			allWords = append(allWords, splitByCase...)
		}
	}

	if len(allWords) == 0 {
		return ""
	}

	lowerCase := cases.Lower(language.English)
	titleCase := cases.Title(language.English)

	// First word should be entirely lowercase
	allWords[0] = titleCase.String(allWords[0])

	// Subsequent words should have their first letter capitalized
	for i := 1; i < len(allWords); i++ {
		allWords[i] = titleCase.String(lowerCase.String(allWords[i]))
	}

	return strings.Join(allWords, "")
}
