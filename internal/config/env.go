package config

import (
	"os"
	"regexp"
)

// envVarPattern matches ${VAR_NAME} placeholders.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR_NAME} placeholders with their environment
// values. A placeholder for an unset variable is left untouched (rather
// than silently becoming "") so a missing secret is easy to spot in a
// dumped config instead of quietly turning into an empty API key.
func expandEnv(raw []byte) []byte {
	return envVarPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		name := envVarPattern.FindSubmatch(match)[1]
		if val, ok := os.LookupEnv(string(name)); ok {
			return []byte(val)
		}
		return match
	})
}
