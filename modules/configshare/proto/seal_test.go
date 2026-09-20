package proto

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rzbdz/newgate/lib/i18n"
)

func TestDeriveKeysDomainSeparatedAndStable(t *testing.T) {
	root := bytes.Repeat([]byte{0x2b}, RootKeyLen)
	enc1, tok1, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	enc2, tok2, err := DeriveKeys(root)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	// 同一把根密钥必须每次都派生出同一对值——否则两台机器各算各的，什么都开不了。
	if !bytes.Equal(enc1, enc2) || !bytes.Equal(tok1, tok2) {
		t.Fatal("同一把根密钥派生出了不同的值（HKDF 不该是这样）")
	}
	// 用途分离：加密密钥与令牌必须不同。**这是这个派生存在的全部意义**——
	// 令牌会进 header、进日志、进任何中间件；如果令牌就是加密密钥，读到日志的
	// 人顺手就拿到了所有订阅的明文。
	if bytes.Equal(enc1, tok1) {
		t.Fatal("派生出的加密密钥与鉴权令牌相同：用途分离没生效，令牌泄露即密钥泄露")
	}
	if len(enc1) != 32 || len(tok1) != 32 {
		t.Fatalf("派生长度不对：enc=%d auth=%d，都该是 32", len(enc1), len(tok1))
	}
	// 换根密钥就必须换出完全不同的值。
	other := bytes.Repeat([]byte{0x2c}, RootKeyLen)
	enc3, tok3, _ := DeriveKeys(other)
	if bytes.Equal(enc1, enc3) || bytes.Equal(tok1, tok3) {
		t.Fatal("换了根密钥却派生出一样的值")
	}
}

func TestDeriveKeysRejectsShortRoot(t *testing.T) {
	if _, _, err := DeriveKeys(bytes.Repeat([]byte{1}, RootKeyLen-1)); err == nil {
		t.Fatal("短于 32 字节的根密钥该被拒绝")
	} else if !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("错误文案该说清长度，实际: %v", err)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	plain := []byte(`{"hello":"世界","n":9007199254740993}`)
	env, err := Seal(encKey, KindConfig, "host-a", 42, plain)
	if err != nil {
		t.Fatalf("封印失败: %v", err)
	}
	if env.Gen != 42 || env.Kind != KindConfig || env.Host != "host-a" {
		t.Fatalf("信封头上不对: %+v", env)
	}
	got, err := Open(encKey, KindConfig, env)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	// 逐字节相等：大整数不能被 JSON 往返改写成 …92（CLAUDE.md §4 的坑）。
	if !bytes.Equal(got, plain) {
		t.Fatalf("往返后内容变了:\n 原 %s\n 回 %s", plain, got)
	}
}

func TestSealTamperDetected(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	env, _ := Seal(encKey, KindConfig, "h", 1, []byte("payload"))

	flip := func(e *Envelope) *Envelope {
		cp := *e
		cp.CT = append([]byte(nil), e.CT...)
		cp.CT[0] ^= 0x01
		return &cp
	}
	if _, err := Open(encKey, KindConfig, flip(env)); !errors.Is(err, ErrTampered) {
		t.Fatalf("密文改一个字节必须解密失败，实际: %v", err)
	}
	// nonce 也是被认证的一部分。
	cp := *env
	cp.Nonce = append([]byte(nil), env.Nonce...)
	cp.Nonce[0] ^= 0x01
	if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
		t.Fatalf("nonce 改一个字节必须解密失败，实际: %v", err)
	}
	// 换一把根密钥（= 两台机器 trust 了不同的 blob）：症状是 ErrTampered，
	// 文案里必须提到根密钥，否则用户会去怀疑网络。
	wrong, _, _ := DeriveKeys(bytes.Repeat([]byte{8}, RootKeyLen))
	if _, err := Open(wrong, KindConfig, env); !errors.Is(err, ErrTampered) {
		t.Fatalf("换根密钥必须解密失败，实际: %v", err)
	}
	if !strings.Contains(i18n.ID(ErrTampered), "root key") {
		t.Fatalf("ErrTampered 的文案该提到根密钥：%v", ErrTampered)
	}
}

// TestSealBindsContext 是 AAD 的意义所在：密文本身是可以被搬运的，绑进
// (版本, 通道, 权威, 代数) 之后，搬运就开不了。
func TestSealBindsContext(t *testing.T) {
	encKey, _, _ := DeriveKeys(bytes.Repeat([]byte{7}, RootKeyLen))
	env, _ := Seal(encKey, KindConfig, "host-a", 42, []byte("payload"))

	t.Run("串道", func(t *testing.T) {
		// 把 /config 的信封放到 /secrets 的位置上：两个端点的密文格式是同一个，
		// 不绑通道就互相能开。
		if _, err := Open(encKey, KindSecrets, env); err == nil {
			t.Fatal("配置通道的信封不该能在密钥通道上打开")
		}
	})
	t.Run("回放旧代", func(t *testing.T) {
		cp := *env
		cp.Gen = 41 // 把第 42 代重放成第 41 代，让副本回滚
		if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
			t.Fatalf("改代数必须解密失败，实际: %v", err)
		}
	})
	t.Run("张冠李戴", func(t *testing.T) {
		cp := *env
		cp.Host = "host-b" // Host 是明文，不绑进 AAD 的话它就能被随便改
		if _, err := Open(encKey, KindConfig, &cp); !errors.Is(err, ErrTampered) {
			t.Fatalf("改权威身份必须解密失败，实际: %v", err)
		}
	})
	t.Run("降级", func(t *testing.T) {
		cp := *env
		cp.V = ProtocolVersion + 1
		_, err := Open(encKey, KindConfig, &cp)
		if err == nil || errors.Is(err, ErrTampered) {
			t.Fatalf("协议版本不匹配该给一句人话（而不是 ErrTampered），实际: %v", err)
		}
		if !strings.Contains(err.Error(), "protocol version") {
			t.Fatalf("版本不匹配的文案该说清是版本问题: %v", err)
		}
	})
}

func TestRootKeyParsing(t *testing.T) {
	root, err := GenerateRootKey()
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if len(root) != RootKeyLen {
		t.Fatalf("生成的根密钥长度 %d，应为 %d", len(root), RootKeyLen)
	}
	blob := RootKeyBlob(root)
	// blob 就是**可直接粘贴**的那串文本：64 个十六进制字符、没有别的修饰。
	// 这条手工分发路径是整个设计里唯一"每台机器各动一次"的操作，它必须一步都
	// 不能错——所以这里锁住"打出来的东西就是能贴过去的东西"。
	if len(blob) != RootKeyLen*2 {
		t.Fatalf("blob 该是 %d 个字符，实际 %d: %q", RootKeyLen*2, len(blob), blob)
	}
	if strings.ContainsAny(blob, " \n\t") {
		t.Fatalf("blob 里不该有空白（粘贴时会被带进去）: %q", blob)
	}
	back, err := ParseRootKey(blob)
	if err != nil {
		t.Fatalf("解析 hex blob 失败: %v", err)
	}
	if !bytes.Equal(back, root) {
		t.Fatal("hex 往返不一致")
	}
	// 32 字节原文也认（有人用 head -c 32 /dev/urandom > root.key 造密钥）。
	if raw, err := ParseRootKey(string(root)); err != nil || !bytes.Equal(raw, root) {
		t.Fatalf("该接受 32 字节原文，实际 err=%v", err)
	}

	// 上面那条**靠随机字节掷骰子**：root 每次都不一样，绝大多数时候首尾不是空白，
	// 所以它长时间掩盖了一个真 bug——ParseRootKey 曾经把 TrimSpace 的结果拿去判
	// 「是不是 32 字节原文」，于是首尾字节落在空白集合里时长度掉到 30/31，这条路
	// 时灵时不灵。2026-09-18 CI 掷到了那个面，红了；本地复现不了。
	//
	// 下面这几条用**确定的**输入把那个面钉死，不再依赖运气。
	// 填充字节刻意选 0x88 / 0x99 / 0xaa：既不是十六进制位（否则整串会被当成 hex
	// 去 decode，测的就不是这条路了——第一版写 0x33，而 0x33 就是字符 '3'，
	// 测试当场自己踩了这个坑），也不是空白，还都不是合法 UTF-8 起始字节。
	for _, raw := range [][]byte{
		append(append([]byte{' '}, bytes.Repeat([]byte{0x88}, RootKeyLen-2)...), '\n'),
		append(append([]byte{'\t'}, bytes.Repeat([]byte{0x99}, RootKeyLen-2)...), ' '),
		append(append([]byte{'\r'}, bytes.Repeat([]byte{0xaa}, RootKeyLen-2)...), 0x0b),
	} {
		got, err := ParseRootKey(string(raw))
		if err != nil {
			t.Fatalf("首尾是空白的 32 字节原文必须被接受（CI 红的根因），输入 %q: %v", raw, err)
		}
		// **逐字节相等**，不是「长度对就行」：trim 掉一个真属于密钥的字节会得到
		// 一把不同的密钥，两台机器派生不一致，症状是「解密失败」——而那句话会
		// 把人引向怀疑网络。静默换掉密钥是这条路径最该避免的失败。
		if !bytes.Equal(got, raw) {
			t.Fatalf("原文被改动了：\n 得到 %x\n 想要 %x", got, raw)
		}
	}

	// 末尾多一个换行要认（echo / 编辑器常见）——且只认这一个。
	withNL := append(bytes.Repeat([]byte{0x88}, RootKeyLen), '\n')
	if got, err := ParseRootKey(string(withNL)); err != nil || !bytes.Equal(got, withNL[:RootKeyLen]) {
		t.Fatalf("32 字节原文 + 末尾换行该被接受，实际 err=%v", err)
	}

	// 截断的 hex 必须说清是长度问题，而不是让用户去怀疑协议。
	if _, err := ParseRootKey(blob[:40]); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("截断的 blob 该给长度提示，实际: %v", err)
	}
	if _, err := ParseRootKey("   "); !errors.Is(err, ErrNoRootKey) {
		t.Fatalf("空串该是 ErrNoRootKey，实际: %v", err)
	}
}

func TestRootKeyFingerprint(t *testing.T) {
	a := bytes.Repeat([]byte{1}, RootKeyLen)
	b := bytes.Repeat([]byte{2}, RootKeyLen)
	fp := RootKeyFingerprint(a)
	if len(fp) != 8 {
		t.Fatalf("指纹该是 8 个字符，实际 %q", fp)
	}
	if fp == RootKeyFingerprint(b) {
		t.Fatal("不同根密钥的指纹不该相同——它是用来核对两台机器拷的是不是同一把的")
	}
	if fp != RootKeyFingerprint(a) {
		t.Fatal("同一把根密钥的指纹必须稳定")
	}
}

func TestSaveLoadRootKey(t *testing.T) {
	sandbox(t)
	root := bytes.Repeat([]byte{9}, RootKeyLen)
	if _, err := LoadRootKey(); !errors.Is(err, ErrNoRootKey) {
		t.Fatalf("还没写过就该是 ErrNoRootKey，实际: %v", err)
	}
	if err := SaveRootKey(root); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	back, err := LoadRootKey()
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if !bytes.Equal(back, root) {
		t.Fatal("根密钥落盘往返不一致")
	}
}
