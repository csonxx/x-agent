package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

type Parser struct {
	reader *bufio.Reader
}

func NewParser(reader io.Reader) *Parser {
	return &Parser{reader: bufio.NewReader(reader)}
}

func (p *Parser) Next() (string, []byte, error) {
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

		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}

		if errors.Is(err, io.EOF) {
			if len(dataLines) == 0 && eventName == "" {
				return "", nil, io.EOF
			}
			return eventName, []byte(strings.Join(dataLines, "\n")), nil
		}
	}
}
