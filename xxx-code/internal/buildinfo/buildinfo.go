package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
	BuiltBy = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	BuiltBy   string `json:"built_by"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

func Current() Info {
	return Info{
		Version:   normalize(Version, "dev"),
		Commit:    normalize(Commit, "unknown"),
		Date:      normalize(Date, "unknown"),
		BuiltBy:   normalize(BuiltBy, "unknown"),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func String() string {
	info := Current()
	return fmt.Sprintf(
		"xxx-code %s\ncommit: %s\nbuilt: %s\nbuilt by: %s\ngo: %s\nplatform: %s\n",
		info.Version,
		info.Commit,
		info.Date,
		info.BuiltBy,
		info.GoVersion,
		info.Platform,
	)
}

func normalize(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
