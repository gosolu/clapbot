// Package builtin provides common built-in tools for agents:
// read_file, write_file, edit_file, list_dir, and shell.
package builtin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gosolu/react"
	"github.com/gosolu/react/pkg/utf8util"
)

// Option configures builtin tools.
type Option func(*config)

type config struct {
	baseDir     string
	maxFileSize int64
	timeout     time.Duration
	workDir     string
}

// WithBaseDir confines file operations to paths within dir.
func WithBaseDir(dir string) Option {
	return func(c *config) { c.baseDir = dir }
}

// WithMaxFileSize sets maximum file read size (default 512KB).
func WithMaxFileSize(n int64) Option {
	return func(c *config) { c.maxFileSize = n }
}

// WithTimeout sets custom execution timeout (e.g. for Shell).
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// WithWorkDir sets working directory for command execution.
func WithWorkDir(dir string) Option {
	return func(c *config) { c.workDir = dir }
}

func parseOpts(opts []Option) config {
	var cfg config
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return cfg
}

func resolvePath(baseDir, path string) (string, error) {
	clean := filepath.Clean(path)
	if baseDir == "" {
		return clean, nil
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		realBase = absBase
	}

	target := clean
	if !filepath.IsAbs(target) {
		target = filepath.Join(absBase, target)
	} else {
		target = filepath.Clean(target)
	}
	rel, err := filepath.Rel(absBase, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("access denied: path %q is outside allowed directory %q", path, baseDir)
	}

	// Verify symlinks do not escape baseDir
	realTarget := evalSymlinksAncestor(target)
	realRel, err := filepath.Rel(realBase, realTarget)
	if err != nil || strings.HasPrefix(realRel, "..") || realRel == ".." {
		return "", fmt.Errorf("access denied: path %q resolves outside allowed directory %q", path, baseDir)
	}

	return target, nil
}

func evalSymlinksAncestor(p string) string {
	if realPath, err := filepath.EvalSymlinks(p); err == nil {
		return realPath
	}
	dir := filepath.Dir(p)
	if dir == p || dir == "." || dir == "/" {
		return p
	}
	realDir := evalSymlinksAncestor(dir)
	return filepath.Join(realDir, filepath.Base(p))
}

type readFileInput struct {
	Path string `json:"path" desc:"The path of the file to read"`
}

// ReadFile returns a tool to inspect file contents. Marked ReadOnly.
func ReadFile(opts ...Option) react.Tool {
	cfg := parseOpts(opts)
	maxSize := cfg.maxFileSize
	if maxSize <= 0 {
		maxSize = 512 * 1024
	}

	return react.NewTool(
		"read_file",
		"Read the entire contents of a file at the given path.",
		func(ctx context.Context, in readFileInput) (string, error) {
			target, err := resolvePath(cfg.baseDir, in.Path)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(target)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("%s is a directory", in.Path)
			}
			f, err := os.Open(target)
			if err != nil {
				return "", err
			}
			defer func() { _ = f.Close() }()

			data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
			if err != nil {
				return "", err
			}
			if int64(len(data)) > maxSize {
				truncated := utf8util.TruncateBytes(data, int(maxSize))
				return string(truncated) + "\n...[truncated]", nil
			}
			return string(data), nil
		},
		react.ReadOnly(),
	)
}

type writeFileInput struct {
	Path    string `json:"path" desc:"The path of the file to write"`
	Content string `json:"content" desc:"The content to write into the file"`
}

// WriteFile returns a tool to create or overwrite files. Marked Destructive.
func WriteFile(opts ...Option) react.Tool {
	cfg := parseOpts(opts)
	return react.NewTool(
		"write_file",
		"Write text content to a file. Overwrites existing files and creates parent directories if needed.",
		func(ctx context.Context, in writeFileInput) (string, error) {
			target, err := resolvePath(cfg.baseDir, in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", err
			}
			if err := os.WriteFile(target, []byte(in.Content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully wrote %d bytes to %s", len(in.Content), target), nil
		},
		react.Destructive(),
	)
}

type editFileInput struct {
	Path      string `json:"path" desc:"The path of the file to edit"`
	OldString string `json:"old_string" desc:"The exact unique text chunk to replace"`
	NewString string `json:"new_string" desc:"The replacement text"`
}

// EditFile returns a tool to safely replace unique chunks of text in a file. Marked Destructive.
func EditFile(opts ...Option) react.Tool {
	cfg := parseOpts(opts)
	return react.NewTool(
		"edit_file",
		"Perform a surgical search-and-replace of an exact text chunk in a file. old_string must appear exactly once.",
		func(ctx context.Context, in editFileInput) (string, error) {
			target, err := resolvePath(cfg.baseDir, in.Path)
			if err != nil {
				return "", err
			}
			if in.OldString == "" {
				return "", fmt.Errorf("old_string cannot be empty")
			}

			info, err := os.Stat(target)
			if err != nil {
				return "", err
			}
			perm := info.Mode().Perm()

			contentBytes, err := os.ReadFile(target)
			if err != nil {
				return "", err
			}
			content := string(contentBytes)

			count := strings.Count(content, in.OldString)
			if count == 0 {
				slog.ErrorContext(ctx, "edit_file: old_string not found", "path", target)
				return "", fmt.Errorf("old_string not found in %s", target)
			}
			if count > 1 {
				slog.ErrorContext(ctx, "edit_file: old_string matches multiple occurrences", "count", count, "path", target)
				return "", fmt.Errorf("old_string matches %d occurrences in %s; must be uniquely identifiable", count, target)
			}

			updated := strings.Replace(content, in.OldString, in.NewString, 1)
			if err := os.WriteFile(target, []byte(updated), perm); err != nil {
				return "", fmt.Errorf("failed to write updated file: %w", err)
			}
			return fmt.Sprintf("Successfully edited %s", target), nil
		},
		react.Destructive(),
	)
}

type listDirInput struct {
	Path string `json:"path" desc:"The directory path to list"`
}

// ListDir returns a tool to list directory entries. Marked ReadOnly.
func ListDir(opts ...Option) react.Tool {
	cfg := parseOpts(opts)
	return react.NewTool(
		"list_dir",
		"List files and subdirectories in a directory path.",
		func(ctx context.Context, in listDirInput) (string, error) {
			p := in.Path
			if p == "" {
				p = "."
			}
			target, err := resolvePath(cfg.baseDir, p)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				return "", err
			}
			var lines []string
			for _, e := range entries {
				kind := "file"
				if e.IsDir() {
					kind = "dir"
				}
				lines = append(lines, fmt.Sprintf("%-5s %s", kind, e.Name()))
			}
			if len(lines) == 0 {
				return "(empty directory)", nil
			}
			return strings.Join(lines, "\n"), nil
		},
		react.ReadOnly(),
	)
}

type shellInput struct {
	Command string `json:"command" desc:"The shell command to execute"`
}

// Shell returns a tool to execute arbitrary bash/sh commands. Marked Destructive and Sequential.
func Shell(opts ...Option) react.Tool {
	cfg := parseOpts(opts)
	timeout := cfg.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return react.NewTool(
		"shell",
		"Execute a command in the system shell. Returns stdout and stderr.",
		func(ctx context.Context, in shellInput) (string, error) {
			if strings.TrimSpace(in.Command) == "" {
				return "", fmt.Errorf("command cannot be empty")
			}
			shell := "bash"
			if _, err := exec.LookPath("bash"); err != nil {
				shell = "sh"
			}

			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(cctx, shell, "-c", in.Command)
			if cfg.workDir != "" {
				cmd.Dir = cfg.workDir
			}
			var outBuf, errBuf bytes.Buffer
			cmd.Stdout = &outBuf
			cmd.Stderr = &errBuf

			runErr := cmd.Run()
			out := outBuf.String()
			errStr := errBuf.String()

			var res strings.Builder
			if out != "" {
				res.WriteString(out)
			}
			if errStr != "" {
				if res.Len() > 0 {
					res.WriteString("\n[stderr]:\n")
				}
				res.WriteString(errStr)
			}

			if runErr != nil {
				slog.ErrorContext(ctx, "shell command failed", "command", in.Command, "error", runErr)
				if res.Len() > 0 {
					return res.String(), fmt.Errorf("exit status: %w", runErr)
				}
				return fmt.Sprintf("failed to execute: %v", runErr), runErr
			}
			if res.Len() == 0 {
				return "(command executed with no output)", nil
			}
			return res.String(), nil
		},
		react.Destructive(),
		react.Sequential(),
	)
}

// NewFilesystem returns standard file tools confined to baseDir.
func NewFilesystem(baseDir string) []react.Tool {
	return []react.Tool{
		ReadFile(WithBaseDir(baseDir)),
		WriteFile(WithBaseDir(baseDir)),
		EditFile(WithBaseDir(baseDir)),
		ListDir(WithBaseDir(baseDir)),
	}
}
