package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParserSkipsCommentsAndCombinesDataLines(t *testing.T) {
	parser := NewParser(strings.NewReader(": keepalive\n\n:event ignored by comment prefix\nevent: delta\ndata: hello\ndata: world\n\n"))

	event, data, err := parser.Next()
	if err != nil {
		t.Fatal(err)
	}
	if event != "delta" {
		t.Fatalf("unexpected event name: %q", event)
	}
	if string(data) != "hello\nworld" {
		t.Fatalf("unexpected event data: %q", string(data))
	}
}

func TestParserFlushesPendingEventAtEOF(t *testing.T) {
	parser := NewParser(strings.NewReader("event: done\ndata: final payload"))

	event, data, err := parser.Next()
	if err != nil {
		t.Fatal(err)
	}
	if event != "done" {
		t.Fatalf("unexpected event name: %q", event)
	}
	if string(data) != "final payload" {
		t.Fatalf("unexpected event data: %q", string(data))
	}

	_, _, err = parser.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after final event, got %v", err)
	}
}

func TestParserReturnsEOFForEmptyInput(t *testing.T) {
	parser := NewParser(strings.NewReader(""))

	_, _, err := parser.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF for empty input, got %v", err)
	}
}
