package proto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// 用途分离用的 info 串。它们是**协议的一部分**：改了等于换了协议，新旧两端会
// 互相解不开——这正是想要的（不做静默降级）。末尾的 v1 是留给将来的第二条
// 轮换路径（换根密钥是第一条）。
const (
	infoEncKey    = "newgate-configshare-enc-v1"
	infoAuthToken = "newgate-configshare-auth-v1"

	// RootKeyLen 根密钥长度。32 字节 = HKDF-SHA256 一个输出块，也是 AES-256
	// 的密钥长度。`newgate config secrets init` 生成的就是这么长的一串随机。
	RootKeyLen = 32
)

// ErrTampered 表示信封没能通过 AEAD 认证。
//
// 三个成因在这一层**无法区分**（这正是 AEAD 的性质，也是它的价值）：内容被
// 改过、两端的根密钥不是同一把、两端的协议版本不一致。给用户看的文案要把三种
// 可能都说出来，否则第一次部署时最难查的那种错（两台机器 trust 了不同的 blob）
// 会表现成一句"解密失败"，让人去怀疑网络。
var ErrTampered = errors.New("信封未通过认证（内容被改动、两端根密钥不同、或协议版本不一致）")

// DeriveKeys 从预共享根密钥派生 (AES-GCM 密钥, bearer token)。
//
// 为什么要派生两把而不是直接用根密钥（HKDF 在这个设计里的全部意义）：
// token 每一发请求都进 Authorization 头，于是它会落到宿主 daemon 的日志、
// 任何反向代理/中间件的日志、以及抓包里。如果 token 就是加密密钥，那么
// **读日志的人顺手就拿到了所有订阅的明文**。派生之后 token 泄露推不出加密
// 密钥——这就是用途分离（domain separation）买到的东西。
//
// salt 传 nil 是有意的：根密钥是**预共享的高熵随机值**（不是口令），不需要
// 靠 salt 抗预计算；而两端必须独立算出同一个值，salt 就不能每次随机。
func DeriveKeys(root []byte) (encKey, authToken []byte, err error) {
	if len(root) < RootKeyLen {
		// 短了几乎总是"config trust 时少拷了一段"或"拷进来的是 base64 而这里
		// 按原始字节用"。说清楚长度比 "invalid key" 有用。
		return nil, nil, fmt.Errorf("根密钥只有 %d 字节，至少要 %d（config trust 给的那串）", len(root), RootKeyLen)
	}
	encKey, err = hkdf.Key(sha256.New, root, nil, infoEncKey, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("派生加密密钥失败: %w", err)
	}
	authToken, err = hkdf.Key(sha256.New, root, nil, infoAuthToken, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("派生鉴权令牌失败: %w", err)
	}
	return encKey, authToken, nil
}

// aad 是这个信封的"关联数据"：不加密、但参与认证。
//
// 把 (协议版本, 通道, 权威身份, 代数) 绑进来，堵掉四种"信封本身是合法的、只是
// 被挪了位"的攻击，它们都不需要知道密钥：
//   - **降级**：把 v1 的信封喂给将来只认 v2 的解析器（或反过来）；
//   - **串道**：把 /config 的信封放到 /secrets 的位置上（两个端点的密文格式
//     是同一个，不绑通道就互相能开）；
//   - **回放旧代**：把第 41 代的信封重放成第 42 代，让副本回滚到旧配置；
//   - **张冠李戴**：把 A 宿主的信封贴上 B 宿主的 Host 标签（Host 是明文，
//     不绑进 AAD 的话它就是一个可以随便改的自由字段，而副本正是靠它判断
//     "权威换了吗"）。
//
// 这些正是"AEAD 保护的是完整性"这句话的具体内容——只加密不绑上下文，密文
// 本身是可以被搬运的。
func aad(kind, host string, gen int) []byte {
	return []byte(fmt.Sprintf("newgate/%s/v%d/host%s/gen%d", kind, ProtocolVersion, host, gen))
}

// Seal 把明文封成信封。kind 用 KindConfig / KindSecrets，host 是发布者的权威
// 身份（宿主自己的 host_id，与服务端记账里那个必须是同一个）。
func Seal(encKey []byte, kind, host string, gen int, plaintext []byte) (*Envelope, error) {
	gcm, err := newGCM(encKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	// 随机 nonce 而不是计数器：GCM 的 nonce 复用会**灾难性地**泄露明文异或，
	// 而且计数器要求两端持久化同步的状态，宿主一重装就会重放。随机 nonce 在
	// 这个量级（一天几十次）下碰撞概率可以忽略。
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("取随机数失败: %w", err)
	}
	return &Envelope{
		V:     ProtocolVersion,
		Kind:  kind,
		Host:  host,
		Gen:   gen,
		Nonce: nonce,
		CT:    gcm.Seal(nil, nonce, plaintext, aad(kind, host, gen)),
	}, nil
}

// Open 解开信封。kind 是调用方**期望**的通道——不是从信封里读的，因为信封
// 说自己是谁不算数，AAD 才是判据。
//
// host 不在这里校验：调用方读 env.Host 与自己的记账比对（那是"权威换了吗"的
// 业务判断，不是"这封信完整吗"的密码学判断）。AAD 只保证 env.Host 与密文是
// 一起被封出来的，改一个字节就开不了。
func Open(encKey []byte, kind string, env *Envelope) ([]byte, error) {
	if env == nil {
		return nil, errors.New("信封是空的")
	}
	// 版本/通道先单独判：这两种错是"配置错了"，文案该说人话；混进 ErrTampered
	// 里就变成一句吓人的"内容被改过"。
	if env.V != ProtocolVersion {
		return nil, fmt.Errorf("对端协议版本是 %d，本二进制只认 %d（两端版本要一起升）", env.V, ProtocolVersion)
	}
	if env.Kind != kind {
		return nil, fmt.Errorf("信封自称是 %q 通道，这里要的是 %q", env.Kind, kind)
	}
	gcm, err := newGCM(encKey)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w（nonce 长度 %d，应为 %d）", ErrTampered, len(env.Nonce), gcm.NonceSize())
	}
	plain, err := gcm.Open(nil, env.Nonce, env.CT, aad(kind, env.Host, env.Gen))
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("AES 密钥不可用: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("GCM 不可用: %w", err)
	}
	return gcm, nil
}
