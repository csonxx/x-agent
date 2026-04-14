package auth

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseTokensSupportsPlaintextAndJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "plaintext", raw: "one\ntwo\nthree", want: []string{"one", "two", "three"}},
		{name: "comma separated", raw: "one, two, three", want: []string{"one", "two", "three"}},
		{name: "json array", raw: `["one","two","three"]`, want: []string{"one", "two", "three"}},
		{name: "json object", raw: `{"tokens":["one","two"],"token":"three"}`, want: []string{"one", "two", "three"}},
		{name: "json string", raw: `"one"`, want: []string{"one"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTokens([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected tokens: got=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestCurrentTokensMergesFileAndStaticToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("file-a\nfile-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := CurrentTokens("static", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file-a", "file-b", "static"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected merged tokens: got=%+v want=%+v", got, want)
	}
}

func TestCurrentTokenReturnsFirstAvailableToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("file-a\nfile-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := CurrentToken("static", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != "file-a" {
		t.Fatalf("unexpected current token: %q", got)
	}
}
