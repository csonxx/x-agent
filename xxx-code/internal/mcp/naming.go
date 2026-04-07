package mcp

import (
	"fmt"
	"hash/fnv"
	"strings"
)

const claudeAIServerPrefix = "claude.ai "

func buildToolName(serverName, toolName string) string {
	server := boundedSegment(serverName, 40, "server")
	tool := boundedSegment(toolName, 80, "tool")
	full := fmt.Sprintf("mcp__%s__%s", server, tool)
	if len(full) <= 128 {
		return full
	}

	available := 128 - len("mcp____") - len(server)
	if available < 12 {
		available = 12
	}
	tool = boundedSegment(toolName, available, "tool")
	return fmt.Sprintf("mcp__%s__%s", server, tool)
}

func boundedSegment(name string, limit int, fallback string) string {
	normalized := normalizeNameForMCP(name)
	if normalized == "" {
		normalized = fallback
	}
	if limit <= 0 || len(normalized) <= limit {
		return normalized
	}
	if limit <= 9 {
		return normalized[:limit]
	}
	hash := shortHash(name)
	return normalized[:limit-9] + "_" + hash
}

func normalizeNameForMCP(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	normalized := b.String()
	if strings.HasPrefix(name, claudeAIServerPrefix) {
		normalized = strings.Trim(normalized, "_")
		for strings.Contains(normalized, "__") {
			normalized = strings.ReplaceAll(normalized, "__", "_")
		}
	}
	return normalized
}

func shortHash(value string) string {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(value))
	return fmt.Sprintf("%08x", sum.Sum32())
}
