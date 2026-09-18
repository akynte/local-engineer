package firewall

import (
	"fmt"
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{50,}\b`),
}

func credentialPattern(text string) bool {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// CheckDiffSecrets checks newly added lines for high-confidence credential
// formats. Errors name the file but never echo the credential. This is an
// additional finalization gate, not a claim to detect arbitrary secrets.
func CheckDiffSecrets(diff string) error {
	file := "unknown file"
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ ") {
			file = strings.TrimPrefix(line, "+++ ")
			continue
		}
		if !strings.HasPrefix(line, "+") {
			continue
		}
		if credentialPattern(line[1:]) {
			return fmt.Errorf("credential pattern found in added content of %s", file)
		}
	}
	return nil
}

// CheckContentSecrets applies the same patterns to text a model supplied for
// durable storage outside the diff — a memory note, a recorded decision. Those
// routes bypass the finalization gate and are replayed into later prompts, so
// the check belongs at the route rather than only at the commit. The label
// names the route; the credential itself is never repeated.
func CheckContentSecrets(label, text string) error {
	if credentialPattern(text) {
		return fmt.Errorf("credential pattern found in %s; store a reference to the secret, not its value", label)
	}
	return nil
}
