//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInitDBFileModes(t *testing.T) {
	old := restrictUmask()
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "data")
	if code, out, errOut := runCLI("init", "--data", dir); code != 0 {
		t.Fatalf("init code=%d out=%q err=%q", code, out, errOut)
	}
	for _, name := range []string{"zkscan.db", "zkscan.db-wal", "zkscan.db-shm", passwordFile} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			if name == "zkscan.db" || name == passwordFile {
				t.Fatal(err)
			}
			continue // -wal/-shm อาจถูกลบตอนปิดฐานข้อมูล
		}
		if fi.Mode().Perm()&^0o640 != 0 {
			t.Errorf("%s สิทธิ์ %v หลวมกว่า 0640", name, fi.Mode().Perm())
		}
	}
}
