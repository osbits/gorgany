package util

import (
	"github.com/spf13/viper"
	path2 "path"
)

func AssetPath(path string, absolute bool) string {
	assetPath := path2.Join("public", "assets", path)
	if absolute {
		p := viper.GetString("app.server.url") + "/" + assetPath
		return p
	}
	return assetPath
}

func PublicPath(path string, absolute bool) string {
	if absolute {
		return path2.Join(viper.GetString("app.server.url"), "public", path)
	}
	return path2.Join("public", path)
}
