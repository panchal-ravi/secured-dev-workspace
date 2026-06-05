// Package localssh upserts and removes per-workspace managed blocks in the
// developer's local SSH config file. It only makes sense when the portal runs on
// the developer's own machine (the PoC mode); in the production NLB-hosted mode
// the backend is remote and this feature is disabled (empty path).
package localssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// markers returns the begin/end sentinels that delimit one workspace's managed
// block, so an upsert replaces exactly that block and never touches the rest of
// the file.
func markers(id string) (begin, end string) {
	return fmt.Sprintf("# >>> portal:%s >>>", id), fmt.Sprintf("# <<< portal:%s <<<", id)
}

// Upsert writes block as the managed section for id, replacing any existing one.
// The file (and ~/.ssh) is created 0600/0700 if absent.
func Upsert(path, id, block string) error {
	begin, end := markers(id)
	managed := begin + "\n" + strings.TrimRight(block, "\n") + "\n" + end + "\n"

	content, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	content = removeBlock(content, begin, end)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return writeFile(path, content+managed)
}

// Remove deletes id's managed block if present; a missing block or file is a
// no-op.
func Remove(path, id string) error {
	begin, end := markers(id)
	content, err := readOrEmpty(path)
	if err != nil {
		return err
	}
	stripped := removeBlock(content, begin, end)
	if stripped == content {
		return nil
	}
	return writeFile(path, stripped)
}

func readOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("localssh: read %s: %w", path, err)
	}
	return string(b), nil
}

// removeBlock returns content with the begin..end block (and the newlines that
// padded it) removed. A begin without a matching end is left untouched.
func removeBlock(content, begin, end string) string {
	bi := strings.Index(content, begin)
	if bi < 0 {
		return content
	}
	rel := strings.Index(content[bi:], end)
	if rel < 0 {
		return content // malformed; do not corrupt the file
	}
	ei := bi + rel + len(end)
	head := strings.TrimRight(content[:bi], "\n")
	tail := strings.TrimPrefix(content[ei:], "\n")
	if head != "" && tail != "" {
		return head + "\n" + tail
	}
	return head + tail
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("localssh: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".portal.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("localssh: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("localssh: replace %s: %w", path, err)
	}
	return nil
}
