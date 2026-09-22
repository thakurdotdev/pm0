//go:build linux

// --redact-env (compat.md §4): when the daemon-wide flag is on, values of
// env keys matching the pinned pattern are replaced with "[redacted]" in
// every jlist/describe/env output and in dump.json. The real values still
// reach the child. Redaction is all-or-nothing in v1.
package daemon

import "regexp"

const redactedPlaceholder = "[redacted]"

// envRedactRe is the §4 pattern, applied to KEY NAMES.
var envRedactRe = regexp.MustCompile(`(?i)(pass|pwd|secret|token|key|credential|auth)`)

// redactEnvMap returns a copy with matching values replaced. Input is not
// mutated (the plain config must stay intact for the dump of OTHER apps
// and for restart --update-env round-trips).
func redactEnvMap(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if envRedactRe.MatchString(k) {
			out[k] = redactedPlaceholder
			continue
		}
		out[k] = v
	}
	return out
}

// redactConfigEnv applies §4 redaction to an app's extra-env config copy
// (used before persisting dump.json).
func redactConfigEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return env
	}
	return redactEnvMap(env)
}
