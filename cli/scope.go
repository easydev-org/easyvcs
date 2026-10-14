package main

import "strings"

// scopeFlag extracts the value of a `--path <scope>` / `--path=<scope>` flag
// from an argument list, returning "" when absent. It is the CLI entry to the
// sub-repo scope model (docs/subrepo-scopes.md).
func scopeFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--path" || a == "-path":
			if i+1 < len(args) {
				return strings.Trim(strings.TrimSpace(args[i+1]), "/")
			}
		case strings.HasPrefix(a, "--path="):
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(a, "--path=")), "/")
		}
	}
	return ""
}

// commitMessage extracts the `-m/--message <text>` flag, defaulting to def.
func commitMessage(args []string, def string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m" || a == "--message" || a == "-message":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "--message="):
			return strings.TrimPrefix(a, "--message=")
		}
	}
	return def
}

// stripScope rewrites a root-relative path into a scope-relative one, returning
// ("", false) when the path is NOT under the scope. A path exactly equal to the
// scope has no file component and is skipped.
func stripScope(scope, path string) (string, bool) {
	if scope == "" {
		return path, true
	}
	prefix := scope + "/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", false
	}
	return rest, true
}
