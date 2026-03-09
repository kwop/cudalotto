package tui

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/kwop/cudalotto/internal"
	"github.com/kwop/cudalotto/stats"
)

var blocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Run starts the TUI and blocks until quit is closed.
func Run(st *stats.Stats, quit <-chan struct{}) {
	fmt.Print("\033[?1049h\033[?25l") // alternate screen, hide cursor
	defer fmt.Print("\033[?25h\033[?1049l")

	render(st)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
			render(st)
		}
	}
}

func render(st *stats.Stats) {
	w, h := termSize()
	inner := w - 4
	if inner < 40 {
		inner = 40
	}

	var b strings.Builder
	b.WriteString("\033[H") // cursor home

	// Title
	titlePad := inner - 10
	if titlePad < 0 {
		titlePad = 0
	}
	wln(&b, "\033[2m┌─\033[0m\033[1m cudalotto \033[0m\033[2m"+strings.Repeat("─", titlePad)+"─┐\033[0m")

	// Status + Uptime
	status := "\033[32m● Mining\033[0m"
	if !st.Connected() {
		status = "\033[31m○ Disconnected\033[0m"
	}
	row(&b, twocol(status, "Uptime: \033[36m"+st.FormatUptime()+"\033[0m", inner), inner)

	// Pool + Difficulty
	row(&b, twocol(
		"Pool: \033[36m"+st.PoolAddr()+"\033[0m",
		fmt.Sprintf("Diff: \033[33m%.0f\033[0m", st.Difficulty()),
		inner,
	), inner)

	// Job + Extranonce2
	jobID := st.JobID()
	if len(jobID) > 18 {
		jobID = jobID[:8] + "…" + jobID[len(jobID)-6:]
	}
	row(&b, twocol(
		"Job: \033[36m"+jobID+"\033[0m",
		fmt.Sprintf("EN2: \033[36m%d\033[0m", st.Extranonce2()),
		inner,
	), inner)

	sep(&b, inner)

	// Hashrate
	row(&b, "Hashrate: \033[1;32m"+internal.FormatHashrate(st.Hashrate())+"\033[0m", inner)

	// Sparkline
	hist := st.HashrateHistory()
	row(&b, "\033[32m"+sparkline(hist, inner)+"\033[0m", inner)

	sep(&b, inner)

	// Stats grid
	row(&b, twocol(
		fmt.Sprintf("Shares sent:    \033[33m%d\033[0m", st.SharesSent.Load()),
		fmt.Sprintf("Total hashes: \033[36m%s\033[0m", fmtBig(st.TotalHashes.Load())),
		inner,
	), inner)
	row(&b, twocol(
		fmt.Sprintf("✓ Accepted:     \033[32m%d\033[0m", st.SharesAccepted.Load()),
		fmt.Sprintf("Jobs received: \033[36m%d\033[0m", st.JobsReceived.Load()),
		inner,
	), inner)
	row(&b, twocol(
		fmt.Sprintf("✗ Rejected:     \033[31m%d\033[0m", st.SharesRejected.Load()),
		fmt.Sprintf("Reconnections: \033[33m%d\033[0m", st.Reconnections.Load()),
		inner,
	), inner)

	sep(&b, inner)

	// Log area
	logH := h - 14
	if logH < 3 {
		logH = 3
	}
	if logH > 20 {
		logH = 20
	}
	logs := st.LogLines(logH)
	for i := 0; i < logH; i++ {
		line := ""
		if i < len(logs) {
			line = logs[i]
			runes := []rune(line)
			if len(runes) > inner {
				line = string(runes[:inner-1]) + "…"
			}
		}
		row(&b, line, inner)
	}

	// Bottom
	wln(&b, "\033[2m└"+strings.Repeat("─", inner+2)+"┘\033[0m")

	os.Stdout.WriteString(b.String())
}

func row(b *strings.Builder, content string, inner int) {
	wln(b, "\033[2m│\033[0m "+pad(content, inner)+" \033[2m│\033[0m")
}

func sep(b *strings.Builder, inner int) {
	wln(b, "\033[2m├"+strings.Repeat("─", inner+2)+"┤\033[0m")
}

func wln(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteString("\033[K\n")
}

func pad(s string, width int) string {
	vw := vwidth(s)
	if vw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vw)
}

func vwidth(s string) int {
	n := 0
	esc := false
	for _, r := range s {
		if r == '\033' {
			esc = true
			continue
		}
		if esc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				esc = false
			}
			continue
		}
		n++
	}
	return n
}

func twocol(left, right string, width int) string {
	lw := vwidth(left)
	rw := vwidth(right)
	gap := width - lw - rw
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + right
}

func sparkline(data []float64, width int) string {
	if len(data) == 0 {
		return strings.Repeat(" ", width)
	}

	start := 0
	if len(data) > width {
		start = len(data) - width
	}
	data = data[start:]

	mn, mx := data[0], data[0]
	for _, v := range data {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}

	var sb strings.Builder
	for _, v := range data {
		idx := 3
		if mx > mn {
			idx = int((v - mn) / (mx - mn) * 7)
		}
		if idx > 7 {
			idx = 7
		}
		sb.WriteRune(blocks[idx])
	}
	for i := len(data); i < width; i++ {
		sb.WriteRune(' ')
	}
	return sb.String()
}

func fmtBig(n uint64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.2fT", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.2fK", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func termSize() (int, int) {
	var sz struct {
		rows, cols, xpix, ypix uint16
	}
	syscall.Syscall(syscall.SYS_IOCTL,
		os.Stdout.Fd(),
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&sz)))
	w, h := int(sz.cols), int(sz.rows)
	if w == 0 {
		w = 80
	}
	if h == 0 {
		h = 24
	}
	return w, h
}
