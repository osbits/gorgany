package config

import (
	"github.com/spf13/viper"
	"os"
	"strings"
)

func Parse(files ...string) error {
	for _, file := range files {
		dir, fileName := parsePath(file)
		viper.AddConfigPath(dir)
		viper.SetConfigName(fileName)
		err := viper.MergeInConfig()
		if err != nil {
			return err
		}
	}

	for _, k := range viper.AllKeys() {
		value := viper.Get(k)
		val, ok := value.(string)
		if ok && strings.HasPrefix(val, "${") && strings.HasSuffix(val, "}") {
			envValue := os.Getenv(strings.TrimSuffix(strings.TrimPrefix(val, "${"), "}"))
			viper.Set(k, envValue)
		} else {
			viper.Set(k, value)
		}
	}

	return nil
}

func parsePath(fullPath string) (string, string) {
	splitPath := strings.Split(fullPath, "/")
	return strings.Join(splitPath[:len(splitPath)-1], "/"), splitPath[len(splitPath)-1]
}
