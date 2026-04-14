package buildinfo

import (
	"strings"
	"testing"
)

func TestCurrentAlwaysReturnsRuntimeMetadata(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.Date == "" || info.BuiltBy == "" {
		t.Fatalf("expected normalized build info, got %+v", info)
	}
	if info.GoVersion == "" || info.Platform == "" {
		t.Fatalf("expected runtime metadata, got %+v", info)
	}
}

func TestStringIncludesNormalizedBuildMetadata(t *testing.T) {
	output := String()
	for _, needle := range []string{
		"xxx-code ",
		"commit: ",
		"built: ",
		"built by: ",
		"go: ",
		"platform: ",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected build info string to contain %q, got %q", needle, output)
		}
	}
}
