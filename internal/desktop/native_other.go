//go:build !windows

package desktop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// 非 Windows 平台：没有原生对话框和剪贴板文件，界面会改用网页里的选择框和手动输入路径。
var errUnsupported = errors.New("当前平台不支持")

func pickFiles(title string, filter []fileFilter, multi bool) ([]string, error) {
	return nil, errUnsupported
}

func pickFolder(title string) (string, error) { return "", errUnsupported }

func clipboardFiles() ([]string, error) { return nil, errUnsupported }

func openPath(p string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, p).Start()
}

func picturesDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Pictures")
	}
	return os.TempDir()
}

func showError(title, msg string) {}

// runWindow 在非 Windows 平台没有内嵌窗口，返回 false 让调用方改用浏览器。
func runWindow(url, title, dataDir string) bool { return false }

func nativeSupported() bool { return false }

// waitBrowserMode 浏览器模式下一直运行到 Ctrl+C。
func waitBrowserMode(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func installerSupported() bool { return false }

func quitApp() { os.Exit(0) }

func bringToFront() {}

func allowForeground(pid int) {}

// AcquireSingleInstance 在非 Windows 平台不做单实例限制。
func AcquireSingleInstance() bool { return true }
