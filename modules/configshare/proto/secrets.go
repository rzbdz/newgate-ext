package proto

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
)

// SecretsFileName 是密钥通道落盘的文件。
//
// 它在配置目录下，但**不属于**托管配置集合（managed.go 的 allowlist 里没有
// 它）：它有独立的记账、独立的代数、独立的权限（0600），因为它是唯一一份
// 明文密钥。把它混进配置集合会让"配置有没有漂移"和"密钥有没有漂移"纠缠在
// 一起，而这两件事的变更频率差一个数量级。
const SecretsFileName = "secrets.json"

// SecretsFormat 是 secrets.json 的格式版本（与线上协议版本无关，是本文件的
// 结构版本；将来字段含义变了就 +1，读到更高的版本号一律拒绝）。
const SecretsFormat = 1

// SecretsFile 是 secrets.json 的落盘形状。
type SecretsFile struct {
	Version int               `json:"version"`
	Keys    map[string]string `json:"keys"` // provider 名 → 明文 key
}

// LoadSecretsFile 读密钥文件。文件不存在返回空表（**不是错误**）：绝大多数
// 机器在 `config join` 之前都没有它，而且宿主自己也可能一个密钥都不用。
func LoadSecretsFile(path string) (SecretsFile, error) {
	empty := SecretsFile{Version: SecretsFormat, Keys: map[string]string{}}
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, err
	}
	var f SecretsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return empty, fmt.Errorf("%s 解析失败: %w", path, err)
	}
	if f.Keys == nil {
		f.Keys = map[string]string{}
	}
	return f, nil
}

// LookupSecret 查一个 provider 的明文密钥。
//
// 这是 store.LoadProviders 的密钥来源（优先级表见 docs/13-config-share.md）：
// 命中就用它，没命中回落到 api_key_env → os.Getenv，再没有就用 provider 自己的
// 内联 api_key（那条路已废弃）。它在**每次** Load() 时被调用，而 watcher 每次
// 变化都会 Load()——所以**轮换密钥不需要重启 daemon**。
//
// 这里**故意**不返回 error（调用方是热路径，且这是 fail-open 的一环）：读不动
// 或解析失败时返回 false，让上层回落到环境变量——宁可某一档因为没 key 而报
// "没有 api_key"（一个响亮、可查的错），也不能因为一个坏文件让整份配置加载
// 失败、把**所有**档位一起打掉。要诊断这个文件本身，走 LoadSecretsFile（它
// 返回 error），config status / doctor 用的是那一个。
func LookupSecret(path, provider string) (string, bool) {
	f, err := LoadSecretsFile(path)
	if err != nil {
		return "", false
	}
	v, ok := f.Keys[provider]
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// secretsBytes 是密钥文件的**规范化字节**。
//
// 漂移比对（client.go）与落盘（MaterializeSecrets）必须用同一份字节算哈希，
// 否则"要不要写"和"变了没有"会用两套判据，表现为每轮都认为变了（写出同样的
// 内容、碰 mtime、触发一次假的配置热更新）。所以规范化只有这一个实现。
func secretsBytes(keys map[string]string) ([]byte, error) {
	f := SecretsFile{Version: SecretsFormat, Keys: keys}
	if f.Keys == nil {
		f.Keys = map[string]string{}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// MaterializeSecrets 落盘密钥，返回内容的 sha256（记账用）。权限 0600：
// 谁能读它谁就能直接花掉那份订阅，这跟 providers.json（0660，组内可见）
// 不是一件事。
func MaterializeSecrets(path string, keys map[string]string) (string, error) {
	raw, err := secretsBytes(keys)
	if err != nil {
		return "", err
	}
	sum := hashBytes(raw)
	// 内容没变就不写文件：不碰 mtime，也不给 watcher 制造一次假变化
	// （"空转完全静默"是硬要求）。
	if cur, err := readFileIfExists(path); err == nil && cur != nil && hashBytes(cur) == sum {
		return sum, nil
	}
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return "", err
	}
	return sum, nil
}
