package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type PermissionPolicy struct {
	ReadRoots           []string
	WriteRoots          []string
	AllowedTools        []string
	BlockedTools        []string
	BashAllowedPrefixes []string
	BashBlockedPrefixes []string
	ReadOnly            bool
	BashEnabled         bool
}

func (r *Runner) EnsureReadPath(path string) error {
	return r.ensurePathAllowed(path, false)
}

func (r *Runner) EnsureWritePath(path string) error {
	return r.ensurePathAllowed(path, true)
}

func (r *Runner) EnsureBash(command string) error {
	if r == nil {
		return nil
	}
	policy := r.PermissionPolicy()
	if !policy.BashEnabled {
		return fmt.Errorf("bash tool is disabled by policy")
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("command is required")
	}
	if err := r.EnsureTool("bash"); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(command)
	if policy.ReadOnly && bashCommandMayWrite(trimmed) {
		return fmt.Errorf("bash command %q may modify files and is blocked by read-only mode", trimmed)
	}
	if matchesAnyPrefix(trimmed, policy.BashBlockedPrefixes) {
		return fmt.Errorf("bash command %q is blocked by policy", trimmed)
	}
	if len(policy.BashAllowedPrefixes) > 0 && !matchesAnyPrefix(trimmed, policy.BashAllowedPrefixes) {
		return fmt.Errorf("bash command %q does not match any allowed command prefix", trimmed)
	}
	return nil
}

func bashCommandMayWrite(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if hasBashWriteRedirection(command) {
		return true
	}

	fields := strings.FieldsFunc(command, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("|&;()", r)
	})
	for i := 0; i < len(fields); i++ {
		token := strings.ToLower(strings.TrimSpace(fields[i]))
		switch token {
		case "tee", "touch", "mkdir", "rmdir", "rm", "mv", "cp", "ln", "chmod", "chown", "truncate", "install":
			return true
		case "sed":
			if i+1 < len(fields) && strings.HasPrefix(strings.ToLower(fields[i+1]), "-i") {
				return true
			}
		case "perl":
			if i+1 < len(fields) && strings.Contains(strings.ToLower(fields[i+1]), "-pi") {
				return true
			}
		case "git":
			if i+1 >= len(fields) {
				continue
			}
			switch strings.ToLower(fields[i+1]) {
			case "add", "am", "apply", "checkout", "cherry-pick", "clean", "clone", "commit", "init", "merge", "mv", "pull", "rebase", "restore", "revert", "rm", "stash", "switch":
				return true
			}
		}
	}
	return false
}

func hasBashWriteRedirection(command string) bool {
	for i := 0; i < len(command); i++ {
		if command[i] != '>' {
			continue
		}
		if i+1 < len(command) && command[i+1] == '&' {
			continue
		}
		if i+1 < len(command) && command[i+1] == '>' {
			if i+2 < len(command) && command[i+2] == '&' {
				continue
			}
			return true
		}
		return true
	}
	return false
}

func (r *Runner) EnsureTool(name string) error {
	if r == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("tool name is required")
	}

	policy := r.PermissionPolicy()
	if containsValue(policy.BlockedTools, name) {
		return fmt.Errorf("tool %s is blocked by policy", name)
	}
	if len(policy.AllowedTools) > 0 && !containsValue(policy.AllowedTools, name) {
		return fmt.Errorf("tool %s is not in the allowed tool list", name)
	}
	return nil
}

func (r *Runner) PermissionPolicy() PermissionPolicy {
	policy := r.config.PermissionPolicy
	policy.ReadRoots = normalizeRoots(policy.ReadRoots)
	policy.WriteRoots = normalizeRoots(policy.WriteRoots)
	policy.AllowedTools = normalizeValues(policy.AllowedTools)
	policy.BlockedTools = normalizeValues(policy.BlockedTools)
	policy.BashAllowedPrefixes = normalizeValues(policy.BashAllowedPrefixes)
	policy.BashBlockedPrefixes = normalizeValues(policy.BashBlockedPrefixes)
	return policy
}

func (r *Runner) ensurePathAllowed(path string, write bool) error {
	if r == nil {
		return nil
	}

	policy := r.PermissionPolicy()
	target, err := normalizePath(path)
	if err != nil {
		return err
	}

	if write {
		if policy.ReadOnly {
			return fmt.Errorf("write access is disabled by read-only mode")
		}
		if len(policy.WriteRoots) == 0 {
			return nil
		}
		if pathWithinAnyRoot(target, policy.WriteRoots) {
			return nil
		}
		return fmt.Errorf("path %s is outside allowed write roots", target)
	}

	if len(policy.ReadRoots) == 0 {
		return nil
	}
	if pathWithinAnyRoot(target, policy.ReadRoots) {
		return nil
	}
	return fmt.Errorf("path %s is outside allowed read roots", target)
}

func normalizeRoots(roots []string) []string {
	normalized := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		path, err := normalizePath(root)
		if err != nil || path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized
}

func normalizePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func pathWithinAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, target)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return true
	}
	return false
}

func normalizeValues(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func matchesAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
