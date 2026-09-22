// Interpreter resolution (compat.md row 3): pm0 is language-agnostic.
// PM2 always wraps scripts in node; pm0 resolves per script:
//
//  1. explicit interpreter ("none" or a binary) wins;
//  2. auto: a shebang line means the KERNEL can exec the script directly —
//     no interpreter needed;
//  3. auto without shebang: extension map (.js/.mjs/.cjs → node, .py →
//     python3, .sh → sh, .rb → ruby, .php → php); unknown extensions fall
//     back to direct exec, which surfaces a clean ENOEXEC as a spawn error
//     instead of silently guessing;
//  4. a .js file with no shebang resolves to node with node_args
//     (row 5 alias for interpreter_args), so the PM2 path is identical.
//
// The result fills App.ExecPath/App.ExecArgs; App.Interpreter carries the
// effective interpreter name for jlist echo.
package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extInterpreters is the extension → interpreter map (row 3 policy).
var extInterpreters = map[string]string{
	".js":   "node",
	".mjs":  "node",
	".cjs":  "node",
	".py":   "python3",
	".sh":   "sh",
	".bash": "bash",
	".rb":   "ruby",
	".php":  "php",
	".pl":   "perl",
}

// ResolveInterpreter computes the exec target for cfg. It mutates nothing;
// the caller copies the results into cfg.ExecPath/ExecArgs/Interpreter.
//
// Returned effective value:
//
//   - "none" or explicit binary: echoed as-is ("none" for direct exec);
//   - auto + shebang: "none" (kernel resolves the shebang);
//   - auto + extension match: the interpreter name ("node", "python3", ...).
//
// An interpreter that is neither "none" nor an existing binary path is
// looked up in PATH at resolution time so a typo fails at `start`, not at
// first spawn.
func ResolveInterpreter(cfg App) (execPath string, execArgs []string, effective string, err error) {
	explicit := strings.TrimSpace(cfg.Interpreter)

	if explicit == "" {
		// Auto: shebang first (the kernel handles it — direct exec).
		if _, ok := hasShebang(cfg.Script); ok {
			return "", nil, "none", nil
		}
		ext := strings.ToLower(filepath.Ext(cfg.Script))
		if interp, ok := extInterpreters[ext]; ok {
			return resolveInterpBin(interp, cfg.Script, cfg.InterpreterArgs)
		}
		// Unknown extension, no shebang: direct exec. ENOEXEC surfaces as a
		// spawn error (parked errored) — loud, per the compat contract.
		return "", nil, "none", nil
	}

	if explicit == "none" {
		return "", nil, "none", nil
	}

	// Explicit interpreter binary (row 5: node_args alias already merged
	// into InterpreterArgs by the CLI layer).
	bin, err := lookPath(explicit)
	if err != nil {
		return "", nil, "", err
	}
	args := make([]string, 0, len(cfg.InterpreterArgs)+1)
	args = append(args, cfg.InterpreterArgs...)
	args = append(args, cfg.Script)
	return bin, args, explicit, nil
}

// resolveInterpBin pins an extension-mapped interpreter: resolve the
// binary, prepend interpreter args, and put the script first among the
// arguments (node script.js ...). node_args is accepted as an alias.
func resolveInterpBin(interp, script string, interpArgs []string) (string, []string, string, error) {
	bin, err := lookPath(interp)
	if err != nil {
		return "", nil, "", err
	}
	args := make([]string, 0, len(interpArgs)+1)
	args = append(args, interpArgs...)
	args = append(args, script)
	return bin, args, interp, nil
}

// WithResolvedExec fills cfg's resolved-exec fields from its interpreter
// policy and returns the effective interpreter name (for echo).
func WithResolvedExec(cfg App) (App, error) {
	if cfg.Script == "" {
		return cfg, fmt.Errorf("config: empty Script")
	}
	switch cfg.ExecMode {
	case "", "fork":
		cfg.ExecMode = "fork"
	case "cluster":
		// M6 row 6: cluster instances are node children sharing one port
		// through the reuseport pool. PM2 refuses to cluster non-node apps;
		// drift is loud at start, never at first spawn.
		cfg.ExecMode = "cluster"
	default:
		return cfg, fmt.Errorf("config: exec_mode %q not supported (fork|cluster)", cfg.ExecMode)
	}
	if cfg.Instances < 1 {
		return cfg, fmt.Errorf("config: instances must be >= 1")
	}
	if !filepath.IsAbs(cfg.Script) {
		var candidate string
		if cfg.Cwd != "" {
			candidate = filepath.Join(cfg.Cwd, cfg.Script)
		} else {
			candidate = cfg.Script
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return cfg, fmt.Errorf("abs %s: %w", candidate, err)
			}
			cfg.Script = abs
		} else if !strings.Contains(cfg.Script, "/") && !strings.Contains(cfg.Script, "\\") {
			if bin, err := exec.LookPath(cfg.Script); err == nil {
				cfg.Script = bin
			} else {
				abs, err := filepath.Abs(candidate)
				if err != nil {
					return cfg, fmt.Errorf("abs %s: %w", candidate, err)
				}
				cfg.Script = abs
			}
		} else {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return cfg, fmt.Errorf("abs %s: %w", candidate, err)
			}
			cfg.Script = abs
		}
	}
	execPath, execArgs, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		return cfg, err
	}
	if cfg.ExecMode == "cluster" {
		base := filepath.Base(strings.ToLower(effective))
		if effective == "" || base != "node" {
			return cfg, fmt.Errorf(
				"config: exec_mode cluster requires a node interpreter, got %q",
				effective)
		}
	}
	cfg.ExecPath = execPath
	cfg.ExecArgs = execArgs
	cfg.Interpreter = effective
	return cfg, nil
}

// hasShebang reports whether the file starts with "#!".
func hasShebang(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	if strings.HasPrefix(line, "#!") {
		return strings.TrimSpace(line), true
	}
	return "", false
}

// lookPath resolves an interpreter to an absolute path: existing file, or
// PATH lookup. Bare names that PATH resolves stay bare (the kernel will
// look them up at exec time too — keeping them bare avoids pinning a
// version the user may have meant loosely); missing names error now.
func lookPath(interp string) (string, error) {
	if strings.ContainsRune(interp, '/') {
		if _, err := os.Stat(interp); err != nil {
			return "", fmt.Errorf("interpreter %s: %w", interp, err)
		}
		return filepath.Clean(interp), nil
	}
	if _, err := exec.LookPath(interp); err != nil {
		return "", fmt.Errorf("interpreter %q not found in PATH (set interpreter: none to exec the script directly)", interp)
	}
	return interp, nil
}
