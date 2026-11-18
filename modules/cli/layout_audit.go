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
	// The two classes not audited, **both declared by the command
	// itself**; the UI lists no names and guesses nothing:
	// - Unstyled: this output is not newgate's layout at all (JSON / raw config / logs);
	// - Handoff: control goes to another process (wrapping a client launch). The previous version was
	//   a `switch args[0]` name table in the UI, and every new command meant coming back to edit it.
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
		// `[layout]`, the command name, the stream name (stdout/stderr), and
		// that raw line are **machine markers** and raw text, kept outside
		// the message: only the statement in the middle is translatable.
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
