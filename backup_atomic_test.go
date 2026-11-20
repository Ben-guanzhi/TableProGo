package main

// 备份原子性的纯逻辑验证（§348）：不依赖外部工具——用注入的"工具写文件"
// 函数模拟成功/失败，验证临时文件→rename 的链路与残片清理。

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteResultSuccess(t *testing.T) {
	out := filepath.Join(t.TempDir(), "backup.sql")
	err := atomicWriteResult(out, func(tmp string) error {
		return os.WriteFile(tmp, []byte("-- complete dump\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil || string(b) != "-- complete dump\n" {
		t.Fatalf("target = %q, err = %v", b, rerr)
	}
	if _, e := os.Stat(out + ".tmp"); e == nil {
		t.Fatal("temp file must be renamed away")
	}
}

func TestAtomicWriteResultFailureCleansTemp(t *testing.T) {
	out := filepath.Join(t.TempDir(), "backup.sql")
	// 目标已有旧备份。
	if err := os.WriteFile(out, []byte("-- previous good backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := atomicWriteResult(out, func(tmp string) error {
		_ = os.WriteFile(tmp, []byte("-- half dump"), 0o600) // 工具写了半截后失败
		return errors.New("dump interrupted")
	})
	if err == nil {
		t.Fatal("the failure must propagate")
	}
	if _, e := os.Stat(out + ".tmp"); e == nil {
		t.Fatal("a failed dump must not leave its half-written temp file")
	}
	b, _ := os.ReadFile(out)
	if string(b) != "-- previous good backup\n" {
		t.Fatalf("the old backup must survive a failed one: %q", b)
	}
}
