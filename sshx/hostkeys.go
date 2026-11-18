package sshx

// SSH 主机密钥校验（TOFU：trust on first use）——不再盲目信任跳板机。
//
// 背景：本文件的 dial 此前用 ssh.InsecureIgnoreHostKey()，注释承认是"与
// 参考产品同款的权衡"。但**同一工作区的另外四个客户端都做了主机密钥校验**
// （dbstudio/db-dev 用 known_hosts、fynequery 用 TOFU store）——唯独本仓
// 跳过，使 SSH 隧道暴露在中间人攻击下（伪造跳板机即可拿到数据库凭据）。
//
// 本实现对齐 fynequery 的 hostKeyStore：addr → base64(key.Marshal()) 的
// 持久化映射，首次连接记录、后续必须一致、变更即拒绝（并给出明确警告）。
// 桌面客户端不能每次弹指纹确认——TOFU 是安全性与可用性之间的正解。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
)

// HostKeyStore 持久化跳板机的公钥（TOFU）。
type HostKeyStore struct {
	mu   sync.Mutex
	path string
}

// NewHostKeyStore 指向持久化文件（启动时调用一次）。
func NewHostKeyStore(path string) *HostKeyStore { return &HostKeyStore{path: path} }

// DefaultHostKeyPath 是默认的主机密钥文件位置（与连接状态同目录）。
func DefaultHostKeyPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	return filepath.Join(base, "tablepro-go", "ssh-known-hosts.json")
}

func (s *HostKeyStore) load() (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return m, nil
}

func (s *HostKeyStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 主机密钥不是机密（公钥），但 0600 与其余状态文件一致。
	return os.WriteFile(s.path, raw, 0o600)
}

// Callback 返回一个 ssh.HostKeyCallback：首次记录、后续比对、变更拒绝。
// 变更时返回的错误明确提示可能是 MITM——用户须手工删除文件里的那一行
// 才允许重新信任（显式动作，不是默默覆盖）。
func (s *HostKeyStore) Callback() ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if key == nil {
			return fmt.Errorf("ssh: %s 未提供主机密钥", hostname)
		}
		encoded := base64.StdEncoding.EncodeToString(key.Marshal())
		s.mu.Lock()
		defer s.mu.Unlock()
		m, err := s.load()
		if err != nil {
			return fmt.Errorf("ssh: 读取 known hosts: %w", err)
		}
		if old, ok := m[hostname]; ok {
			if old != encoded {
				return fmt.Errorf(
					"ssh: %s 的主机密钥已变更——可能是中间人攻击。若确认服务器已重建，请手工删除 %s 中该主机的记录后重试",
					hostname, s.path)
			}
			return nil
		}
		m[hostname] = encoded
		if err := s.save(m); err != nil {
			return fmt.Errorf("ssh: 保存主机密钥失败: %w", err)
		}
		return nil
	}
}

// defaultHostKeys 是进程级的默认 store（dial 使用；测试可替换）。
var defaultHostKeys = NewHostKeyStore(DefaultHostKeyPath())

// SetHostKeyStore 替换默认 store（测试或自定义配置路径）。
func SetHostKeyStore(s *HostKeyStore) { defaultHostKeys = s }
