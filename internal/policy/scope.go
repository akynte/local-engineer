package policy

import (
	"path"
	"path/filepath"
	"strings"
)

// Covers accepts exact paths, directory prefixes and path.Match globs. A /**
// suffix includes descendants. An empty scope grants no model writes.
func Covers(scope []string, rel string) bool {
	for _, pattern := range scope {
		if !validScope(pattern) {
			continue
		}
		pattern = path.Clean(pattern)
		if pattern == "." || matches(pattern, rel) {
			return true
		}
	}
	return false
}

func validScope(pattern string) bool {
	if pattern == "" || filepath.IsAbs(pattern) || strings.Contains(pattern, "\\") {
		return false
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return false
		}
	}
	_, err := path.Match(pattern, "probe")
	return err == nil
}

// Sensitive reports paths whose content must not enter model context.
func Sensitive(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		part = strings.ToLower(part)
		if strings.HasPrefix(part, ".env") || strings.HasSuffix(part, ".pem") ||
			strings.HasSuffix(part, ".key") || strings.Contains(part, "credentials") {
			return true
		}
	}
	return strings.HasPrefix(strings.ToLower(rel), ".agent/secrets")
}
