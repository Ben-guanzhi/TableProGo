package main

// 协作评论的本地持久化（文件 JSONL——append 语义，无需外部服务）。
// 路径可经环境变量覆盖（测试与多工作区隔离）。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type commentEntry struct {
	Hash   string `json:"hash"`
	Author string `json:"author"`
	Text   string `json:"text"`
	At     string `json:"at"`
}

func commentStorePath() (string, error) {
	if p := os.Getenv("NAVOP_COLLAB_STORE"); p != "" {
		return p, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(d, "tablepro", "collab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "comments.jsonl"), nil
}

func hashSQL(sql string) string {
	h := sha256.Sum256([]byte(strings.Join(strings.Fields(sql), " ")))
	return hex.EncodeToString(h[:8])
}

func loadComments(path string) ([]commentEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []commentEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c commentEntry
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			out = append(out, c)
		}
	}
	return out, sc.Err()
}

func saveComments(path string, list []commentEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// append 模式：只追加新增条目（重复保存同一列表会重复——由调用方控制）。
	if len(list) == 0 {
		return nil
	}
	last := list[len(list)-1]
	b, _ := json.Marshal(last)
	_, err = f.Write(append(b, '\n'))
	return err
}
