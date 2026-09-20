package webdashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	i18n "github.com/rzbdz/newgate/lib/i18n"
	"github.com/rzbdz/newgate/modules/config/domain"
	"github.com/rzbdz/newgate/modules/config/paths"
	"github.com/rzbdz/newgate/modules/config/store"
)

// 写路径的全部内容在这里：**快照 + 基线 + 冲突检测**。
//
// # 为什么不是「点一下就写」
//
// 同一个配置文件有两个写者：这个界面，和命令行。用户在浏览器里改档位的这几分钟
// 里，完全可能在另一个终端敲了 `newgate tier`——两边都写，后写的赢，前一个人的
// 改动**静默消失**。这正是要避免的事，所以每次保存都要带上「我这份是从哪个版本
// 改出来的」，落盘前先比对磁盘现状。
//
// # 为什么不阻止，而是检测
//
// 让 CLI 也走这把锁等于要求内核改写入路径（它今天不带锁，注释里自己写着
// last-writer-wins）。而**检测**给出的东西更值钱：它能把冲突的两份内容都摆给
// 用户看，让他自己选——「你的改动被别人覆盖了」比「保存失败，请重试」有用得多。
//
// # 锁保护的是谁
//
// flock 只对**同样走这把锁**的写者有效：两个浏览器标签页同时点保存是真的会撞的
// （读-比对-写之间隔着几毫秒），所以这一段必须原子。CLI 不拿这把锁，它造成的
// 冲突由基线比对**发现**（检查与 rename 之间那个极小的窗口是承认的代价，写进
// 这里而不是假装没有）。

// revPrefix 让基线在人眼前一眼能认出是内容哈希（也是以后换算法时的版本位）。
const revPrefix = "sha256:"

// lockName 是写锁文件，放在配置根下。
//
// 为什么不用 configshare 那把：那是发行版 configshare 模块自己的记账锁，语义是
// 「谁在同步配置仓库」，跟这里「谁在改这份配置」不是一件事，共用一把会互相拖住。
const lockName = ".web.lock"

// revision 是文件内容哈希；文件不存在时返回空串（「基线 = 什么都没有」）。
func revision(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return revPrefix + hex.EncodeToString(sum[:])
}

// ---------- 请求与响应 ----------

type commitRequest struct {
	Ops []commitOp `json:"ops"`
}

type commitOp struct {
	// Kind："file" 整份文本（源文件 tab）、"profile" 只改档位绑定（映射 GUI）、
	// "state" 改 state.json 里的字段（默认 profile 这类全局开关）。
	Kind    string `json:"kind"`
	Path    string `json:"path"` // 相对配置根
	Base    string `json:"base"` // 客户端拿到的 revision，空 = 文件当时不存在
	Content string `json:"content,omitempty"`

	// profile 用
	Roles map[string][]bindingDoc `json:"roles,omitempty"`

	// state 用（只认这几个字段，别的字段一律不碰）
	DefaultProfile *string `json:"default_profile,omitempty"`
}

type conflictDoc struct {
	Path    string `json:"path"`
	Base    string `json:"base"`
	Current string `json:"current"`
	Yours   string `json:"yours"`
	Theirs  string `json:"theirs"`
}

type commitResponse struct {
	OK        bool              `json:"ok"`
	Revisions map[string]string `json:"revisions,omitempty"`
	Conflict  []conflictDoc     `json:"conflict,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// commit 是保存端点的实现。
//
// 全部成功或全部不写：一个 op 撞上冲突就整体拒绝（带上冲突内容）。半套写进去的
// 配置文件比没写更糟——它会让「界面显示档位 A，磁盘上是档位 B」这种状态活下来。
func (h *Handler) commit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		h.writeJSONStatus(w, http.StatusBadRequest, commitResponse{Error: i18n.T("the request body is not valid JSON: {err}", i18n.A{"err": err})})
		return
	}
	if len(req.Ops) == 0 {
		h.writeJSONStatus(w, http.StatusBadRequest, commitResponse{Error: i18n.T("there is nothing to save", nil)})
		return
	}

	resp := commitResponse{Revisions: map[string]string{}}
	// 拿到锁再读盘：不然两个标签页各自读到旧内容、各自通过基线比对，后一个
	// rename 把前一个的改动盖掉。
	_, err := withLock(filepath.Join(paths.Root(), lockName), 3*time.Second, func() error {
		targets := make([]string, 0, len(req.Ops))
		for _, op := range req.Ops {
			p, err := resolveOpPath(op.Path)
			if err != nil {
				return err
			}
			targets = append(targets, p)
		}
		// 先全部比对，一个不过就什么都不写。
		for i, op := range req.Ops {
			cur, err := op.apply(targets[i])
			if err != nil {
				return err
			}
			disk := revision(targets[i])
			if disk != op.Base {
				resp.Conflict = append(resp.Conflict, conflictDoc{
					Path: op.Path, Base: op.Base, Current: disk,
					Yours: string(cur), Theirs: readOrEmpty(targets[i]),
				})
			}
		}
		if len(resp.Conflict) > 0 {
			return nil
		}
		for i, op := range req.Ops {
			cur, err := op.apply(targets[i])
			if err != nil {
				return err
			}
			if err := writeAtomic(targets[i], cur); err != nil {
				return err
			}
			resp.Revisions[op.Path] = revision(targets[i])
		}
		return nil
	})
	if err != nil {
		h.writeJSONStatus(w, http.StatusInternalServerError, commitResponse{Error: err.Error()})
		return
	}
	if len(resp.Conflict) > 0 {
		h.writeJSONStatus(w, http.StatusConflict, resp)
		return
	}
	resp.OK = true
	h.writeJSONStatus(w, http.StatusOK, resp)
}

// resolveOpPath 只接受配置根下的路径。
//
// 这里必须挡：路径是**请求里来的**，而 `../../.ssh/authorized_keys` 也是合法字符串。
// 判据是「拼出来之后仍然在根下面」，不是「看起来像不像正常路径」。
func resolveOpPath(rel string) (string, error) {
	if rel == "" {
		return "", i18n.E("an operation is missing its file path", nil)
	}
	root := paths.Root()
	full := filepath.Join(root, filepath.Clean("/"+rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", i18n.E("the file {path} is outside the configuration directory", i18n.A{"path": rel})
	}
	return full, nil
}

// apply 算出这个 op 要写进去的字节。它**只算不写**——比对阶段要拿它跟磁盘比，
// 冲突时还要把它原样摆给用户看。
func (op commitOp) apply(full string) ([]byte, error) {
	switch op.Kind {
	case "file":
		return []byte(op.Content), nil
	case "profile":
		return applyProfile(full, op.Roles)
	case "state":
		return applyState(full, op)
	}
	return nil, i18n.E("unknown operation kind {kind}", i18n.A{"kind": op.Kind})
}

// applyProfile 只改 roles 这一段，其余字段原样保留。
//
// 为什么不是「把整个 profile 序列化回去」：界面展示的是**概念**（档位、候选），
// 而文件里还有它不认识的东西（extends、window、compact、将来加的字段）。整份
// 重写等于拿界面的知识覆盖文件，用户手写的字段会在他没碰过的地方消失。
func applyProfile(full string, roles map[string][]bindingDoc) ([]byte, error) {
	if strings.HasSuffix(full, ".kv") {
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		p, err := store.ParseProfileKV(string(b))
		if err != nil {
			return nil, err
		}
		p.Roles = toRoles(roles)
		return []byte(store.SerializeProfileKV(p)), nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	// map[string]json.RawMessage：只替换 roles 那一个键，别的键连**解析都不解析**，
	// 于是它们不可能被这次保存改变形状（数字的写法、键的顺序、注释掉的东西）。
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(full)})
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(toRoles(roles))
	if err != nil {
		return nil, err
	}
	doc["roles"] = raw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// applyState 改 state.json 里的字段。
//
// 走 RawMessage 的理由同 applyProfile：state.json 里住着**六个模块**各自的配置
// （`ModuleConfig` 是扁平的兄弟键），整份重写会拿这一处的知识覆盖别人的。
func applyState(full string, op commitOp) ([]byte, error) {
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, i18n.Ef(err, "{file} is not valid JSON", i18n.A{"file": filepath.Base(full)})
	}
	if op.DefaultProfile != nil {
		raw, err := json.Marshal(*op.DefaultProfile)
		if err != nil {
			return nil, err
		}
		doc["default_profile"] = raw
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// toRoles 把请求里的绑定转回 domain 的形状。
//
// 空列表**保留**（写进去一个空档位），不当作「删掉这个键」：界面里把一个档位的
// 候选全删光，意思就是「这一档没有候选」——那与「没写这一档」（于是往上继承
// normal/mid）是两件事，替用户选后者等于改了他没碰过的语义。
func toRoles(roles map[string][]bindingDoc) map[string]domain.Candidates {
	out := map[string]domain.Candidates{}
	for id, list := range roles {
		c := make(domain.Candidates, 0, len(list))
		for _, b := range list {
			c = append(c, domain.Binding{Provider: b.Provider, Model: b.Model, Ref: b.Ref})
		}
		out[id] = c
	}
	return out
}

func readOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// writeAtomic 同目录临时文件 + rename：读者只会看到完整旧版或新版。
//
// 临时名带随机后缀（不是固定的 `.tmp`）：固定名会让两个并发写者撞在同一个 inode
// 上，一个 rename 走了，另一个 rename 的是一份被对方写了一半的内容。
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// 0600 太严（配置目录是多人共享读的），先落 0660 再 rename。
	if err := f.Chmod(0o660); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// withLock 拿建议锁跑 fn（与 configshare/proto.WithLock 同一套判据：fd 一关锁
// 就没，kill -9 也不会留下要人工清的陈旧锁）。
func withLock(path string, wait time.Duration, fn func() error) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o2770); err != nil {
		return false, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o660)
		if err != nil {
			return false, err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			defer func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}()
			return true, fn()
		}
		_ = f.Close()
		if err != syscall.EWOULDBLOCK {
			return false, err
		}
		if !time.Now().Before(deadline) {
			return false, i18n.E("another save is in progress, try again in a moment", nil)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *Handler) writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
