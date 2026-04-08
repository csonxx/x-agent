package buildinfo

import "testing"

func TestCurrentAlwaysReturnsRuntimeMetadata(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.Date == "" || info.BuiltBy == "" {
		t.Fatalf("expected normalized build info, got %+v", info)
	}
	if info.GoVersion == "" || info.Platform == "" {
		t.Fatalf("expected runtime metadata, got %+v", info)
	}
}
