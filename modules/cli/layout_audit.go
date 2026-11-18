package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/rzbdz/newgate-ext/lib/style"
	"github.com/rzbdz/newgate/lib/i18n"
)

type widthViolation struct {
	stream string
	line   int
	width  int
	text   string
}

func shouldAuditLayout(service *service, args []string) bool {
	explicit := os.Getenv("NEWGATE_LAYOUT_AUDIT") != ""
	if len(args) == 0 {
		return explicit || service.verboseOn()
	}
	// 不审计的两类，**都由命令自己声明**，界面不列名单、也不猜：
	//   - Unstyled：这次的输出本来就不是 newgate 的版式（JSON / 原始配置 / 日志）；
	//   - Handoff：控制权要交给别的进程（包装启动一个客户端）。
	// 上一版这里是界面里一张 `switch args[0]` 的名字表，每加一个命令都得回来改。
	if command, ok := lookupCommand(service, args); ok {
		if _, handoff := command.(Handoff); handoff {
			return false
		}
		if unstyled, ok := command.(Unstyled); ok && unstyled.Unstyled(args[1:]) {
			return false
		}
	}
	return explicit || service.verboseOn()
}

func auditLayout(args []string, run func() int) int {
	stdout, stderr := os.Stdout, os.Stderr
	outR, outW, outErr := os.Pipe()
	errR, errW, errErr := os.Pipe()
	if outErr != nil || errErr != nil {
		for _, file := range []*os.File{outR, outW, errR, errW} {
			if file != nil {
				_ = file.Close()
			}
		}
		return run()
	}

	os.Stdout, os.Stderr = outW, errW
	results := make(chan []widthViolation, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go auditStream(&wg, outR, stdout, "stdout", results)
	go auditStream(&wg, errR, stderr, "stderr", results)

	code := run()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = stdout, stderr
	wg.Wait()
	close(results)

	command := "newgate"
	if len(args) > 0 {
		command += " " + args[0]
	}
	var violations []widthViolation
	for batch := range results {
		violations = append(violations, batch...)
	}
	for _, v := range violations {
		// `[layout]`、命令名、流名（stdout/stderr）与那一行原文都是**机器标记**
		// 与原文，留在消息外面：能翻译的只有中间那句陈述。
		fmt.Fprintf(stderr, "[layout] %s %s %s: %s\n",
			command, v.stream,
			i18n.T("line {n} is {w} columns wide (limit {max})",
				i18n.A{"n": v.line, "w": v.width, "max": style.MaxColumns}),
			v.text)
	}
	return code
}

func auditStream(wg *sync.WaitGroup, src, dst *os.File, stream string,
	results chan<- []widthViolation) {
	defer wg.Done()
	defer src.Close()
	reader := bufio.NewReader(src)
	var violations []widthViolation
	line := 0
	for {
		raw, err := reader.ReadString('\n')
		if raw != "" {
			_, _ = io.WriteString(dst, raw)
			text := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
			line++
			if width := style.VisibleWidth(text); width > style.MaxColumns {
				violations = append(violations, widthViolation{
					stream: stream, line: line, width: width,
					text: strings.TrimSpace(text),
				})
			}
		}
		if err != nil {
			break
		}
	}
	results <- violations
}
