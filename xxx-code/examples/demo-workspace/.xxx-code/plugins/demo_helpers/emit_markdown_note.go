package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type inputPayload struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Bullets []string `json:"bullets"`
}

type resultPayload struct {
	Content string `json:"content"`
}

func main() {
	var input inputPayload
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		input = inputPayload{}
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = "Untitled"
	}

	lines := []string{"# " + title}
	if summary := strings.TrimSpace(input.Summary); summary != "" {
		lines = append(lines, "", summary)
	}

	for _, bullet := range input.Bullets {
		bullet = strings.TrimSpace(bullet)
		if bullet == "" {
			continue
		}
		if lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "- "+bullet)
	}

	payload := resultPayload{
		Content: strings.TrimSpace(strings.Join(lines, "\n")) + "\n",
	}
	if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
