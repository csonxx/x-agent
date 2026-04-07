package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	compactionSummaryMarker = "[compacted summary]"
	maxSummaryLines         = 48
	maxSummaryChars         = 6000
)

type CompactionReport struct {
	BeforeTokens   int `json:"before_tokens"`
	AfterTokens    int `json:"after_tokens"`
	BeforeMessages int `json:"before_messages"`
	AfterMessages  int `json:"after_messages"`
}

func EstimateTokens(messages []Message) int {
	total := 0
	for _, msg := range messages {
		total += 4
		for _, block := range msg.Content {
			switch block.Type {
			case BlockText:
				total += estimateTextTokens(block.Text)
			case BlockToolUse:
				total += 12 + estimateTextTokens(block.Name) + estimateTextTokens(formatToolInput(block.Input))
			case BlockToolResult:
				total += 12 + estimateTextTokens(block.Result)
			}
		}
	}
	return total
}

func (r *Runner) CompactSession(session *Session) (CompactionReport, bool) {
	return r.compactSessionIfNeeded(&ExecutionContext{
		Runner:     r,
		Session:    session,
		WorkingDir: r.config.WorkingDir,
	})
}

func (r *Runner) compactSessionIfNeeded(exec *ExecutionContext) (CompactionReport, bool) {
	if r.config.ContextBudget <= 0 || exec == nil || exec.Session == nil {
		return CompactionReport{}, false
	}

	messages := exec.Session.Snapshot()
	beforeTokens := EstimateTokens(messages) + estimateTextTokens(r.config.SystemPrompt)
	if beforeTokens <= r.config.ContextBudget {
		return CompactionReport{}, false
	}

	keep := r.config.CompactKeepMessages
	if keep < 4 {
		keep = 4
	}
	if len(messages) <= keep {
		return CompactionReport{}, false
	}

	prefix := messages[:len(messages)-keep]
	suffix := messages[len(messages)-keep:]
	summary := buildCompactionSummary(prefix)
	compacted := append([]Message{NewTextMessage(RoleAssistant, summary)}, suffix...)
	exec.Session.Replace(compacted)

	report := CompactionReport{
		BeforeTokens:   beforeTokens,
		AfterTokens:    EstimateTokens(compacted) + estimateTextTokens(r.config.SystemPrompt),
		BeforeMessages: len(messages),
		AfterMessages:  len(compacted),
	}

	r.emit(Event{
		Kind:      EventSessionCompacted,
		AgentID:   exec.AgentID,
		AgentName: exec.AgentName,
		Text: fmt.Sprintf(
			"compacted session from ~%d to ~%d tokens (%d -> %d messages)",
			report.BeforeTokens,
			report.AfterTokens,
			report.BeforeMessages,
			report.AfterMessages,
		),
	})

	return report, true
}

func buildCompactionSummary(messages []Message) string {
	lines := make([]string, 0, maxSummaryLines)
	for _, message := range messages {
		lines = append(lines, summarizeMessage(message)...)
		if len(lines) >= maxSummaryLines {
			break
		}
	}

	var builder strings.Builder
	builder.WriteString(compactionSummaryMarker)
	builder.WriteString("\nOlder conversation was compacted to stay within the context budget. Keep this as prior context.\n")

	if len(lines) == 0 {
		builder.WriteString("- Earlier conversation existed but had no compactable text details.\n")
		return builder.String()
	}

	for _, line := range lines {
		if builder.Len()+len(line)+3 > maxSummaryChars {
			builder.WriteString("- Earlier details were truncated during compaction.\n")
			break
		}
		builder.WriteString("- ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}

func summarizeMessage(message Message) []string {
	lines := make([]string, 0, len(message.Content))
	rolePrefix := "User"
	if message.Role == RoleAssistant {
		rolePrefix = "Assistant"
	}

	text := normalizeWhitespace(message.Text())
	if text != "" {
		if strings.HasPrefix(text, compactionSummaryMarker) {
			text = strings.TrimSpace(strings.TrimPrefix(text, compactionSummaryMarker))
			lines = append(lines, "Earlier compacted summary: "+truncateSummaryLine(text))
		} else {
			lines = append(lines, rolePrefix+": "+truncateSummaryLine(text))
		}
	}

	for _, block := range message.Content {
		switch block.Type {
		case BlockToolUse:
			lines = append(lines, fmt.Sprintf(
				"%s tool_use %s %s",
				rolePrefix,
				block.Name,
				truncateSummaryLine(normalizeWhitespace(formatToolInput(block.Input))),
			))
		case BlockToolResult:
			prefix := "Tool result"
			if block.IsError {
				prefix = "Tool error"
			}
			lines = append(lines, fmt.Sprintf("%s: %s", prefix, truncateSummaryLine(normalizeWhitespace(block.Result))))
		}
		if len(lines) >= maxSummaryLines {
			break
		}
	}

	return lines
}

func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateSummaryLine(value string) string {
	const limit = 220
	if utf8.RuneCountInString(value) <= limit {
		return value
	}

	runes := []rune(value)
	return string(runes[:limit]) + "..."
}

func estimateTextTokens(text string) int {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	runes := utf8.RuneCountInString(text)
	return (runes / 4) + 1
}
