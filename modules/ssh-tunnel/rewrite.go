package sshtunnel

import (
	"bytes"
	"net/http"
	"strings"
)

// RemoteUIPrefix 是远端那个界面在自己的机器上的绝对前缀（= web-dashboard 的 Prefix）。
//
// 它同时是**这套改写能成立的全部理由**：远端产物里的绝对地址只有这一个字面量。
// vite.config.ts 的 `base: "/ui/"` 被构建期烘进三个地方——
//
//	dist/index.html   <script src="/ui/assets/index-XXXX.js">
//	dist/index.html   <link rel="icon" href="/ui/favicon.svg">
//	dist/assets/*.js  import.meta.env.BASE_URL 折叠成的 "/ui/api"
//
// 三者都是同一个 "/ui/"，所以**一条文本替换全覆盖**，包括那个让页面白屏的
// /ui/api（浏览器会拿它去打本机自己的 web-dashboard，于是看到一个本地界面里
// 混着远端的数据——比白屏更糟的一种错，因为它看起来是好的）。
const RemoteUIPrefix = "/ui/"

// LocalPrefix 是某个 target 在本机这条 route 上的根，**带尾斜杠**。
//
// 带尾斜杠是必需的：改写的方向是「远端绝对路径 → 本机绝对路径」，而
// `/ui/remote/ds/ui/api` 这种拼法完全取决于这里有没有那个斜杠。少一个斜杠的症状
// 是远端的每个请求都打到 `/ui/remoteds/…` 上——404，而且在浏览器里看不出来。
func LocalPrefix(id string) string {
	return Prefix + "/" + id + "/"
}

// textBodies 是会被改写的那些内容类型。
//
// # 为什么 JS 和 CSS 也在里面（这一条是踩出来的）
//
// 最初的判断是「只动 text/html，因为 CSS 与 JS 是从 HTML 里引来的，它们的**地址**
// 在 HTML 那一遍已经改写过」。前半句对，后半句错：JS 里那个 `/ui/` 不是资源地址，
// 它是 **vite 把 import.meta.env.BASE_URL 烘进产物的结果**，而这个值在运行期被用来
// 拼 API 地址——
//
//	const Fn="/ui/api";   // ← 产物里就长这样
//
// 于是不改它的后果是：隧道里那个页面照着**本机自己的** `/ui/api` 发请求，界面正常
// 渲染，数据全是本机的。用户看到的是一个「能用的界面」——比白屏糟得多，他得先
// 意识到数据不对才可能来查（实测：装配出来的第一天就被抓到）。
//
// CSS 一并放进来是同一条：vite 写进 url() 的也是绝对前缀，虽然它今天只出现在
// 资源地址上（那一条 HTML 里已经改过），但把这个文件的规则写成「按内容类型」
// 而不是「按我心里想的那几种情况」少一次判断。
//
// 判据仍然是**前缀本身**：没含 "/ui/" 的正文一个字节都不动（见 rewritePrefix 的
// 快速返回），所以把 JS 放进来不代表每个 bundle 都要过一遍替换。
var textBodies = map[string]bool{
	"text/html":                true,
	"text/css":                 true,
	"text/javascript":          true,
	"application/javascript":   true,
	"application/x-javascript": true, // 老一点的服务器还在发这个
}

// shouldRewrite 说这份响应该不该改写。
//
// 三条判据，各自防一种具体的错：
//
//   - **内容类型在 textBodies 里**。别的（JSON、图片、字体）一律不动：它们要么是
//     数据而不是地址，要么根本不该被文本替换碰。
//   - **没有 Content-Encoding**。压缩过的正文里没有明文路径可改；改了就毁掉这份
//     响应。调用方已经把 Accept-Encoding 摘掉了（见 proxy.go），所以这条路正常
//     到不了——留着它是为了「远端无论如何都压缩」那种情况：那时宁可**不改**，
//     因为不改的后果是资源 404（看得见、能解释），改坏的后果是一个乱码页面。
//   - **2xx / 3xx / 4xx**。远端的错误页也是它自己那套 HTML，同样需要改写才能正常
//     显示——不改的话，用户在「远端报了个错」时看到的会是一个白屏，而白屏与
//     「route 坏了」长得一模一样。5xx 不动：那种正文通常是代理/网关自己生成的。
func shouldRewrite(resp *http.Response) bool {
	if resp.StatusCode >= 500 {
		return false
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return false
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return textBodies[strings.ToLower(strings.TrimSpace(ct))]
}

// urlDelims 是「一个地址字面量到哪里为止」。
//
// 它服务的判据见 isAbsoluteURL：我们要区分 `/ui/x`（远端自己的绝对路径，该改）与
// `https://example.com/ui/x`（指向别处，改了就把它变成我们自己主机上的一个路径）。
// 后者在正文里只可能出现在引号、括号、标签之间——所以沿这些字符往回退，退到的
// 那一段就是「这个地址的完整写法」。
const urlDelims = " \t\r\n\"'<>()=,;`"

// rewritePrefix 是改写本身：把远端正文里的绝对前缀换成本机这条 route 的前缀。
//
// 为什么是**裸文本替换**而不是一个 HTML tokenizer：tokenizer 只动属性值，动不了
// JS 字符串，而 JS 里那条（`"/ui/api"`）恰恰是让整个界面能工作的那条。而裸替换的
// 误伤面——正文里恰好出现 "/ui/" 四个字符——在远端自己的 HTML 里只可能是路径：
// 那是它自己产物的正文，不是用户数据。
//
// **唯一的例外是带主机的地址**（`https://example.com/ui/x`）：它不是「远端自己的
// 另一个路径」，改它等于把一条出站的链接指回我们自己。裸替换分不出这两者，所以要
// 往回看一眼（见 isAbsoluteURL）。那个判断是这套改写里唯一一点「聪明」，而它换来
// 的是「外链不会被悄悄改道」——一条错了没有任何症状的错。
//
// 取舍的边界写清：**将来远端某个模块往 HTML 里吐用户内容**（比如一个把请求正文
// 显示出来的页面），这条替换就会改到用户数据。到那时要么给改写加一层「只在属性值
// 与字符串字面量里替换」的判据，要么让那条 route 只对 JSON 开放。
func rewritePrefix(body []byte, local string) []byte {
	if !bytes.Contains(body, []byte(RemoteUIPrefix)) {
		// 绝大多数响应（JSON、图片、404 的那一句话）都不含它：一次 Contains 就返回，
		// 不分配新切片。
		return body
	}
	var out []byte
	// cursor = 「已经写出去的正文到哪儿了」，scan = 「找到哪儿了」。两者分开是
	// 因为跳过的那些（外链）仍然要写出去，只是不替换——用一个游标做这件事会让
	// 「跳过」变成「丢掉」。
	cursor, scan := 0, 0
	for {
		i := bytes.Index(body[scan:], []byte(RemoteUIPrefix))
		if i < 0 {
			break
		}
		i += scan
		scan = i + len(RemoteUIPrefix)
		if isAbsoluteURL(body, i) {
			continue
		}
		if out == nil {
			out = make([]byte, 0, len(body)+len(local))
		}
		out = append(out, body[cursor:i]...)
		out = append(out, local...)
		cursor = scan
	}
	if out == nil {
		return body
	}
	return append(out, body[cursor:]...)
}

// isAbsoluteURL 报告这个位置上的 `/ui/` 是不是某个**带主机的地址**的一部分。
//
// 判据只有两条，都是「往回退到分隔符，看那一段长什么样」：
//
//	https://example.com/ui/x   → 那一段里有 "://"  → 是别处
//	//example.com/ui/x          → 那一段以 "//" 开头     → 是别处
//	/ui/assets/x.js            → 那一段就是个路径        → 是远端自己
//
// 不做成完整的 URL 解析：正文是**别人产物的字节**（minified JS 里混着字符串、
// 模板、正则），拿一个解析器去读它只会把「我认不出来」变成「我改错了」。
func isAbsoluteURL(body []byte, at int) bool {
	j := at
	for j > 0 && !strings.ContainsRune(urlDelims, rune(body[j-1])) {
		j--
	}
	token := body[j:at]
	return bytes.Contains(token, []byte("://")) || bytes.HasPrefix(token, []byte("//"))
}

// rewriteLocation 把重定向的 Location 也换掉。
//
// 不换的后果是**跳到本机自己的 /ui/**：远端 BFF 对 `/ui`（没有尾斜杠）会 302 到
// `/ui/`，而那个地址在本机上是 web-dashboard——用户点一下，就从一个「远端的界面」
// 跳到了一个「本地界面」，而两者长得几乎一样。这是这套改写里唯一一处**看不出来**
// 的错，所以单独一个函数、单独一条测试。
//
// 只动**站内绝对路径**（以 "/ui/" 开头的那种）；带 scheme 的绝对地址（http://…）
// 与相对地址一律不碰——前者指向的是别处，后者本来就跟着当前文档走。
func rewriteLocation(loc, local string) string {
	if !strings.HasPrefix(loc, RemoteUIPrefix) {
		return loc
	}
	return local + strings.TrimPrefix(loc, RemoteUIPrefix)
}
