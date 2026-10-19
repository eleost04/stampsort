//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// WebView2 的消息循环必须在创建窗口的线程上跑：把 main goroutine 锁在主线程。
func init() { runtime.LockOSThread() }

var errUnsupported = errors.New("当前平台不支持")

var (
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procDragQueryFileW   = shell32.NewProc("DragQueryFileW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procIsIconic         = user32.NewProc("IsIconic")
	procAllowSetFg       = user32.NewProc("AllowSetForegroundWindow")
)

// mainHwnd 是主窗口句柄，对话框以它为父窗口（模态）。
var (
	mainHwnd uintptr
	mainWV   webview2.WebView
	wvMu     sync.Mutex
)

func nativeSupported() bool { return true }

// ------------------------------------------------------------------ 文件对话框（IFileOpenDialog）

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosAllowMulti      = 0x200
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	sigdnFileSysPath   = 0x80058000
	hrCancelled        = 0x800704C7
	clsctxInproc       = 0x1
	coinitApartment    = 0x2
	coinitNoOLE1DDE    = 0x4

	// IFileOpenDialog vtable 下标（IUnknown 3 + IModalWindow 1 + IFileDialog 23 + IFileOpenDialog 2）
	vShow         = 3
	vSetFileTypes = 4
	vSetOptions   = 9
	vGetOptions   = 10
	vSetTitle     = 17
	vGetResult    = 20
	vGetResults   = 27
	// IShellItem / IShellItemArray
	vGetDisplayName = 5
	vArrGetCount    = 7
	vArrGetItemAt   = 8
	vRelease        = 2
)

type filterSpec struct{ name, spec *uint16 }

// comCall 调用 COM 对象 vtable 中第 idx 个方法。
func comCall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, idx*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

var dialogMu sync.Mutex

// onSTA 在一个新的、初始化了 COM 单线程套间的系统线程上执行 fn。
func onSTA(fn func() ([]string, error)) ([]string, error) {
	dialogMu.Lock()
	defer dialogMu.Unlock()
	type result struct {
		paths []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartment|coinitNoOLE1DDE)
		if !failed(hr) {
			defer procCoUninitialize.Call()
		}
		p, err := fn()
		ch <- result{p, err}
	}()
	r := <-ch
	return r.paths, r.err
}

func showFileDialog(title string, filters []fileFilter, opts uint32) ([]string, error) {
	var dlg unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInproc,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == nil {
		return nil, fmt.Errorf("创建文件对话框失败 0x%08X", uint32(hr))
	}
	defer comCall(dlg, vRelease)

	var cur uint32
	comCall(dlg, vGetOptions, uintptr(unsafe.Pointer(&cur)))
	comCall(dlg, vSetOptions, uintptr(cur|opts|fosForceFileSystem|fosPathMustExist))
	t16, _ := windows.UTF16PtrFromString(title)
	comCall(dlg, vSetTitle, uintptr(unsafe.Pointer(t16)))
	var specs []filterSpec
	for _, f := range filters {
		n, _ := windows.UTF16PtrFromString(f.Name)
		s, _ := windows.UTF16PtrFromString(f.Spec)
		specs = append(specs, filterSpec{n, s})
	}
	if len(specs) > 0 {
		comCall(dlg, vSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	}

	hr = comCall(dlg, vShow, mainHwnd)
	runtime.KeepAlive(t16)
	runtime.KeepAlive(specs)
	if uint32(hr) == hrCancelled {
		return nil, nil
	}
	if failed(hr) {
		return nil, fmt.Errorf("对话框出错 0x%08X", uint32(hr))
	}

	if opts&fosAllowMulti == 0 {
		var item unsafe.Pointer
		if hr := comCall(dlg, vGetResult, uintptr(unsafe.Pointer(&item))); failed(hr) || item == nil {
			return nil, fmt.Errorf("读取选择结果失败 0x%08X", uint32(hr))
		}
		defer comCall(item, vRelease)
		p, err := itemPath(item)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	}
	var arr unsafe.Pointer
	if hr := comCall(dlg, vGetResults, uintptr(unsafe.Pointer(&arr))); failed(hr) || arr == nil {
		return nil, fmt.Errorf("读取选择结果失败 0x%08X", uint32(hr))
	}
	defer comCall(arr, vRelease)
	var n uint32
	comCall(arr, vArrGetCount, uintptr(unsafe.Pointer(&n)))
	var out []string
	for i := uint32(0); i < n; i++ {
		var item unsafe.Pointer
		if failed(comCall(arr, vArrGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item)))) || item == nil {
			continue
		}
		if p, err := itemPath(item); err == nil {
			out = append(out, p)
		}
		comCall(item, vRelease)
	}
	return out, nil
}

func itemPath(item unsafe.Pointer) (string, error) {
	var p *uint16
	if hr := comCall(item, vGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&p))); failed(hr) || p == nil {
		return "", fmt.Errorf("不是文件系统路径 0x%08X", uint32(hr))
	}
	s := windows.UTF16PtrToString(p)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s, nil
}

func pickFiles(title string, filters []fileFilter, multi bool) ([]string, error) {
	var opts uint32 = fosFileMustExist
	if multi {
		opts |= fosAllowMulti
	}
	return onSTA(func() ([]string, error) { return showFileDialog(title, filters, opts) })
}

func pickFolder(title string) (string, error) {
	p, err := onSTA(func() ([]string, error) { return showFileDialog(title, nil, fosPickFolders) })
	if err != nil || len(p) == 0 {
		return "", err
	}
	return p[0], nil
}

// ------------------------------------------------------------------ 剪贴板

const cfHDROP = 15

// clipboardFiles 读取剪贴板里"复制"的文件（资源管理器、微信等），返回原文件路径。
func clipboardFiles() ([]string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	opened := false
	for range 10 { // 其它程序可能正占着剪贴板
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !opened {
		return nil, errors.New("剪贴板被占用")
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfHDROP)
	if h == 0 {
		return nil, nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var out []string
	for i := uintptr(0); i < n; i++ {
		l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		out = append(out, windows.UTF16ToString(buf))
	}
	return out, nil
}

// ------------------------------------------------------------------ 其它

func openPath(p string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	return windows.ShellExecute(windows.Handle(mainHwnd), verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func picturesDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Pictures, 0); err == nil {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h + `\Pictures`
	}
	return os.TempDir()
}

func messageBox(title, msg string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(windows.HWND(mainHwnd), m, t, flags|windows.MB_SETFOREGROUND)
}

func showError(title, msg string) { messageBox(title, msg, windows.MB_OK|windows.MB_ICONERROR) }

// runWindow 创建 WebView2 窗口并运行消息循环，窗口关闭后返回 true；没有 WebView2 运行时返回 false。
func runWindow(url, title, dataDir string) bool {
	os.MkdirAll(dataDir, 0o755)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("STAMPSORT_DEBUG") != "",
		DataPath:  dataDir,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: title, Width: 1180, Height: 780, IconId: 1, Center: true,
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
	wvMu.Lock()
	mainWV = w
	wvMu.Unlock()
	defer func() {
		wvMu.Lock()
		mainWV = nil
		wvMu.Unlock()
	}()
	mainHwnd = uintptr(w.Window())
	w.SetSize(960, 640, webview2.HintMin)
	w.Navigate(url)
	w.Run()
	return true
}

// waitBrowserMode：没有 WebView2 时界面在浏览器里打开，用一个提示框维持程序运行。
func waitBrowserMode(ctx context.Context) error {
	messageBox("stampsort", "没有检测到 Microsoft Edge WebView2，界面已在浏览器中打开。\n\n处理完成前请不要关闭这个提示框，点「确定」即退出程序。\n\n（安装 WebView2 后会改用独立窗口：https://go.microsoft.com/fwlink/p/?LinkId=2124703）",
		windows.MB_OK|windows.MB_ICONINFORMATION)
	return nil
}

// ------------------------------------------------------------------ 更新 / 单实例

func installerSupported() bool { return true }

// quitApp 关闭主窗口让程序正常退出（自动更新时由安装程序接手）。
func quitApp() {
	wvMu.Lock()
	w := mainWV
	wvMu.Unlock()
	if w == nil {
		os.Exit(0)
	}
	w.Dispatch(func() { w.Terminate() })
}

// bringToFront 把主窗口切到前台（最小化时先还原）。
func bringToFront() {
	wvMu.Lock()
	w := mainWV
	wvMu.Unlock()
	if w == nil {
		return
	}
	w.Dispatch(func() {
		if r, _, _ := procIsIconic.Call(mainHwnd); r != 0 {
			procShowWindow.Call(mainHwnd, 9) // SW_RESTORE
		}
		procSetForeground.Call(mainHwnd)
	})
}

// allowForeground 让已运行的实例可以把自己的窗口切到前台（Windows 只允许前台进程转让这个权利）。
func allowForeground(pid int) {
	if pid > 0 {
		procAllowSetFg.Call(uintptr(pid))
	}
}

var instanceMutex windows.Handle

// AcquireSingleInstance 用命名互斥量保证只有一个桌面版窗口。返回 false 表示已经有实例在运行。
func AcquireSingleInstance() bool {
	name, _ := windows.UTF16PtrFromString("Local\\stampsort-desktop")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return false
	}
	instanceMutex = h // 一直持有到进程退出
	return true
}
