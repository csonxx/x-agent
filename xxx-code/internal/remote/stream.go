package remote

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

type sseParser struct {
	reader *bufio.Reader
}

func newSSEParser(reader io.Reader) *sseParser {
	return &sseParser{reader: bufio.NewReader(reader)}
}

func (p *sseParser) Next() (string, []byte, error) {
	var (
		eventName string
		dataLines []string
	)

	for {
		line, err := p.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 && eventName == "" {
				if errors.Is(err, io.EOF) {
					return "", nil, io.EOF
				}
				continue
			}
			return eventName, []byte(strings.Join(dataLines, "\n")), nil
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
		}

		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 && eventName == "" {
				return "", nil, io.EOF
			}
			return eventName, []byte(strings.Join(dataLines, "\n")), nil
		}
	}
}
