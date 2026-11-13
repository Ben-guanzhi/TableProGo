package workbenchhost

// fs.go — the tiny filesystem seams the AI context reader needs. Kept here (not
// inlined) so tests can point them at a temp dir without patching os.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// tempPathFor returns a temp file path under the OS temp dir with a unique name.
func tempPathFor(prefix string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano()))
}

// readFileHead reads at most n bytes of the file.
func readFileHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if err != nil && read == 0 {
		return nil, err
	}
	return buf[:read], nil
}

// removeFile deletes a path, treating "already gone" as success.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
