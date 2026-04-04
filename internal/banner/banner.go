package banner

import (
	"fmt"
	"strings"

	"github.com/JoaoGabrielVianna/lightweight-saas-backend/internal/fonts"
)

const (
	Reset = "\033[0m"

	// 🔹 Foreground (texto)
	Black   = "\033[30m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"

	// 🔹 Bright (mais forte)
	BrightBlack   = "\033[90m"
	BrightRed     = "\033[91m"
	BrightGreen   = "\033[92m"
	BrightYellow  = "\033[93m"
	BrightBlue    = "\033[94m"
	BrightMagenta = "\033[95m"
	BrightCyan    = "\033[96m"
	BrightWhite   = "\033[97m"

	// 🔹 Background
	BgBlack   = "\033[40m"
	BgRed     = "\033[41m"
	BgGreen   = "\033[42m"
	BgYellow  = "\033[43m"
	BgBlue    = "\033[44m"
	BgMagenta = "\033[45m"
	BgCyan    = "\033[46m"
	BgWhite   = "\033[47m"

	// 🔹 Bright Background
	BgBrightBlack   = "\033[100m"
	BgBrightRed     = "\033[101m"
	BgBrightGreen   = "\033[102m"
	BgBrightYellow  = "\033[103m"
	BgBrightBlue    = "\033[104m"
	BgBrightMagenta = "\033[105m"
	BgBrightCyan    = "\033[106m"
	BgBrightWhite   = "\033[107m"
)

// 🔹 Render ASCII Title
func RenderTitle(text string, color string) {
	text = strings.ToUpper(text)

	lines := make([]string, 7)

	for _, char := range text {
		if glyph, ok := fonts.AnsiShadow[char]; ok {
			for i := 0; i < 7; i++ {
				lines[i] += glyph[i]
			}
		} else {
			for i := 0; i < 7; i++ {
				lines[i] += "        "
			}
		}
	}

	fmt.Println()
	for _, line := range lines {
		fmt.Println(color + line + Reset)
	}
	fmt.Println()
}

// 🔹 Render Subtitle
func RenderSubtitle(text string, color string) {
	line := strings.Repeat("=", len(text)+4)

	fmt.Println(color + line + Red)
	fmt.Println(color + "  " + text + "  " + Reset)
	fmt.Println(color + line + Reset)
	fmt.Println()
}
