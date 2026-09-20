// Package proto 是配置共享的线上协议与副本侧的应用逻辑。
//
// 为什么单独一个子包（而不是写在 modules/configshare 根目录）
//
// 启动后台轮询只有一处时机是对的：daemon 的 cli.Serve。而 modules/cli 得
// import 它才能启动它——代码若长在 configshare 根目录，cli → configshare 就会
// 和 configshare → cliapi（注册 `config` 命令）撞成一个 import 环。
//
// 放在子包里依赖方向就是单向的：cli → configshare/proto，而 proto 只认
// paths / config、store、domain 和标准库，不认识任何端口模块（尤其不 import
// cliapi）。附带的好处是本包**可以脱离组件图单独测**：测试全在 httptest +
// 临时目录上跑（见 *_test.go）。
//
// # 协议的形状
//
//	宿主 (role=host)                          副本 (role=client)
//	  GET /__newgate/config   ──── 信封 ────▶  拉 → 解密 → 校验 → 漂移守卫 → 落盘
//	  GET /__newgate/secrets  ──── 信封 ────▶  同一套，另一个通道
//
// 为什么不复用 git / ssh 当传输层（这是设计里被否掉的那条路）：宿主要放到
// tailscale 后面并且要支持代理宿主（跳板），而受限节点不允许入站——跳板链路下
// ssh 的信任模型不成立。所以传输只能是我们自己的一条**只出站**的拉取。
// git 完全留在宿主上做版本管理/回滚/审计（人用的工具），副本不装 git、不 clone。
//
// # 三条贯穿全包的性质
//
//  1. **副本只出站**。没有任何代码会去连副本，受限节点（不能入站）也能当副本。
//  2. **fail-open**。任何失败（网络、解密、schema、漂移）都保留当前配置继续
//     服务。宿主挂了各机照常转发——这是"不引入单点故障"在代码里的落地。
//  3. **不静默**。拒绝应用要说清是哪几个文件、为什么；但也不能刷屏
//     （见 poller.go 的日志政策）。
package proto

// 线上常量。
const (
	// ProtocolVersion 是信封格式版本。改了信封结构就 +1，并且它被绑进 AEAD 的
	// AAD（见 seal.go），所以两端协议不一致时表现为**解密失败**而不是解析出
	// 半个结构体——不做兼容猜测，两端必须一起动。
	ProtocolVersion = 1

	// SupportedSchema 是本二进制能正确解释的最高配置 schema。
	//
	// 为什么它是**唯一有牙的版本字段**：另一个字段 min_newgate_version 装的是
	// `git describe` 串（`build-17-…-gf90dc81`），跟将来的 `build-18-…` 之间
	// **没有数值大小关系**（`--dirty` 更糟），拿它做判断只能是字符串相等。
	// schema 是小整数、手工 bump，才排得出先后。详见 version.go 的两级门。
	SupportedSchema = 1

	// 两个通道的名字。它们同时是信封里的 Kind（绑进 AAD）和端点路径的一段。
	KindConfig  = "config"
	KindSecrets = "secrets"

	// PathConfig / PathSecrets 是宿主 daemon 上的两个端点。
	//
	// 分成两个而不是一个：配置变得勤（加个 provider 就变）、密钥变得稀（一个月
	// 一次），缓存与频率语义不同，审计也该分开看。合并成一个的话，每次改 provider
	// 都要重新传一遍全部密钥。
	PathConfig  = "/__newgate/config"
	PathSecrets = "/__newgate/secrets"
)

// Envelope 是端点上传输的信封：一切明文都在 CT 里，外面只剩路由和代数。
//
// 为什么 Gen 是**明文**却又安全：副本要能在"代数没变"时**连解密都不做**
// （空转完全静默，见 poller.go），所以它必须先读得到代数。而它同时被绑进
// AAD，撒谎说一个别的代数会让认证失败——攻击者能做的只有"谎报一个旧代数让
// 这个副本这轮不更新"，那是拒绝服务，不是密钥泄露，而链路本身已经过
// bearer 鉴权 + AEAD 保护。这是这个取舍成立的前提。
//
// Host 同理是明文，但理由是**正确性**而不是省事：副本判断"权威换了吗"必须
// 在解密之前就能做。权威一换，新宿主的 generation 从 0 重新开始，副本若只比
// 代数就会认为"比我应用过的旧"而**永久拒绝更新**——一个静默的、只有重装宿主
// 才能修的故障。把 Host 放进明文（同时绑进 AAD，所以它不能被篡改或张冠李戴）
// 让那条快路径也能识别权威变更。
type Envelope struct {
	V     int    `json:"v"`
	Kind  string `json:"kind"`
	Host  string `json:"host"`
	Gen   int    `json:"gen"`
	Nonce []byte `json:"nonce"`
	CT    []byte `json:"ct"`
}

// Snapshot 是宿主发布的一份配置快照（信封里的那个明文 JSON）。
//
// Files 的值是 []byte 而不是 string：内容在 JSON 里以 base64 走，落盘时
// **逐字节写**，不做 JSON 往返——这个仓库被"JSON 往返改字节"咬过（大整数变
// `…92`、字段顺序与未知键丢失，见 CLAUDE.md §4），配置快照尤其经不起
// 重排：providers.json 里可能有本二进制不认识的键，重排一次就悄悄丢了。
//
// 键是**相对配置目录的斜杠路径**（`providers.json`、`mappings/prod.json`），
// 受 allowlist 约束（见 managed.go），落盘前还要过 os.Root——payload 来自
// 网络，文件名不该有能力把写入引到配置目录之外。
type Snapshot struct {
	// HostID 是权威身份，宿主首次启动时生成、此后不变。
	//
	// 为什么需要它：Generation 是单调计数，而宿主的记账文件一旦丢失（重装、
	// 换机、误删 host.json），计数会从 0 重来。没有 HostID 的话副本会认为
	// "所有新快照都比我已经应用的旧"，**永久拒绝更新**——一个静默的、只有
	// 重装宿主才能修的故障。有了它，权威换了就是换了，副本重置代数基线。
	HostID     string            `json:"host_id"`
	Generation int               `json:"generation"`
	Version    string            `json:"version,omitempty"` // 内容哈希，审计与"变了没有"用
	Files      map[string][]byte `json:"files"`
}

// Secrets 是宿主发布的密钥集合（另一个通道的明文）。
//
// 为什么密钥单独一个通道而不是塞在配置里：`providers.json` 在宿主侧是 git
// 管的，api_key 一旦提交就永久进历史。所以配置通道**不含密**（用 api_key_env
// 或留空），明文只走这条加密通道，且新增一个 key 只需要宿主操作一次。
type Secrets struct {
	HostID     string            `json:"host_id"`
	Generation int               `json:"generation"`
	Keys       map[string]string `json:"keys"` // provider 名 → 明文 key
}
