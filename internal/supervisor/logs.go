package supervisor

import (
	"os"
	"regexp"
	"strings"
)

var redactPatterns = []*regexp.Regexp{
	// JSON fields from the service key or auth exchanges.
	regexp.MustCompile(`(?i)("(?:private_key|key_id|token|access_token|refresh_token|secret)"\s*:\s*")[^"]*(")`),
	// PEM blocks.
	regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`),
	// JWTs.
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// Bearer / authorization headers.
	regexp.MustCompile(`(?i)(authorization:\s*(?:(?:bearer|basic)\s+)?|bearer\s+)\S+`),
}

// Redact removes the given literal secrets and anything that looks like a key
// or token from s.
func Redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 8 {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	for i, re := range redactPatterns {
		switch i {
		case 0:
			s = re.ReplaceAllString(s, "${1}[REDACTED]${2}")
		case 3:
			s = re.ReplaceAllString(s, "${1}[REDACTED]")
		default:
			s = re.ReplaceAllString(s, "[REDACTED]")
		}
	}
	return s
}

// Tail returns up to the last n lines of the file at path.
func Tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
