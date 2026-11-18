package sshx

// TOFU 主机密钥校验的测试：首次记录、再次一致放行、变更拒绝。

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestHostKeyStoreTOFU(t *testing.T) {
	store := NewHostKeyStore(filepath.Join(t.TempDir(), "known.json"))
	cb := store.Callback()
	addr, _ := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}, net.Addr(nil)
	key := testKey(t)

	// 首次：记录并放行。
	if err := cb("bastion:22", addr, key); err != nil {
		t.Fatalf("首次连接应被信任: %v", err)
	}
	// 再次同键：放行。
	if err := cb("bastion:22", addr, key); err != nil {
		t.Fatalf("相同密钥应放行: %v", err)
	}
	// 文件里确实存了 base64(marshal)。
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString(key.Marshal())
	if !strings.Contains(string(raw), want) {
		t.Fatalf("密钥未持久化: %s", raw)
	}

	// 换一把钥匙：拒绝，且错误提示 MITM 与手工处理。
	other := testKey(t)
	err = cb("bastion:22", addr, other)
	if err == nil || !strings.Contains(err.Error(), "中间人") {
		t.Fatalf("密钥变更应带 MITM 警告拒绝: %v", err)
	}
	// 另一台主机首次：独立记录。
	if err := cb("other:22", addr, other); err != nil {
		t.Fatalf("另一台主机应独立首次信任: %v", err)
	}
}
