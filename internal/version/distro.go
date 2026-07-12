package version

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// Distro names what the box actually runs, one lowercase word: "ubuntu",
// "debian", "macos", "windows", or the os-release ID of anything else.
// Falls back to the bare GOOS when nothing better is known.
func Distro() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		if id := osReleaseID("/etc/os-release"); id != "" {
			return id
		}
		return "linux"
	default:
		return runtime.GOOS
	}
}

func osReleaseID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			return strings.ToLower(strings.Trim(v, `"'`))
		}
	}
	return ""
}
