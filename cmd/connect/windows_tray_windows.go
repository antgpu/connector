//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmDestroy      = 0x0002
	wmSize         = 0x0005
	wmCommand      = 0x0111
	wmVScroll      = 0x0115
	wmTimer        = 0x0113
	wmDpiChanged   = 0x02E0
	wmRButtonUp    = 0x0205
	wmLButtonUp    = 0x0202
	wmContextMenu  = 0x007B
	wmApp          = 0x8000
	wmTrayCallback = wmApp + 1

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfByCommand = 0x00000000
	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGray      = 0x00000001
	mfDisabled  = 0x00000002
	mfEnabled   = 0x00000000

	tpmLeftAlign   = 0x0000
	tpmBottomAlign = 0x0020
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNoNotify    = 0x0080

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsVScroll          = 0x00200000
	wsHScroll          = 0x00100000
	esMultiline        = 0x0004
	esAutoVScroll      = 0x0040
	esAutoHScroll      = 0x0080
	esReadOnly         = 0x0800
	wmSetFont          = 0x0030
	emSetSel           = 0x00B1
	emGetSel           = 0x00B0
	emScrollCaret      = 0x00B7
	emGetFirstVisLine  = 0x00CE
	emLineScroll       = 0x00B6
)

const (
	swShow       = 5
	swShowNormal = 1
)

const (
	sbBottom      = 7
	smCxSmallIcon = 49
	smCySmallIcon = 50
)

const dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

const (
	trayItemStatus = 1001
	trayItemAction = 1002
	trayItemLogs   = 1003

	trayStatusTimerID = 1
	logRefreshTimerID = 2
	trayIconRetryID   = 3
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procPostQuitMessage           = user32.NewProc("PostQuitMessage")
	procGetMessageW               = user32.NewProc("GetMessageW")
	procTranslateMessage          = user32.NewProc("TranslateMessage")
	procDispatchMessageW          = user32.NewProc("DispatchMessageW")
	procCreatePopupMenu           = user32.NewProc("CreatePopupMenu")
	procAppendMenuW               = user32.NewProc("AppendMenuW")
	procModifyMenuW               = user32.NewProc("ModifyMenuW")
	procEnableMenuItem            = user32.NewProc("EnableMenuItem")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procTrackPopupMenu            = user32.NewProc("TrackPopupMenu")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procGetClientRect             = user32.NewProc("GetClientRect")
	procSetTimer                  = user32.NewProc("SetTimer")
	procKillTimer                 = user32.NewProc("KillTimer")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procLoadIconW                 = user32.NewProc("LoadIconW")
	procDestroyIcon               = user32.NewProc("DestroyIcon")
	procDestroyMenu               = user32.NewProc("DestroyMenu")
	procShowWindow                = user32.NewProc("ShowWindow")
	procMoveWindow                = user32.NewProc("MoveWindow")
	procSetWindowTextW            = user32.NewProc("SetWindowTextW")
	procSendMessageW              = user32.NewProc("SendMessageW")
	procGetScrollInfo             = user32.NewProc("GetScrollInfo")
	procRegisterWindowMsgW        = user32.NewProc("RegisterWindowMessageW")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForSystem           = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow           = user32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi    = user32.NewProc("GetSystemMetricsForDpi")

	procCreateFontW  = gdi32.NewProc("CreateFontW")
	procDeleteObject = gdi32.NewProc("DeleteObject")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")

	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")

	trayWndProcCallback uintptr
	taskbarCreatedMsg   uint32

	trayMu   sync.Mutex
	trayInst *windowsTrayApp
)

const swHide = 0

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct {
	X int32
	Y int32
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
}

type windowsTrayApp struct {
	serviceName string
	displayName string
	stateDir    string
	showLogs    bool
	mutex       windows.Handle
	icon        uintptr
	hwnd        uintptr
	menu        uintptr
	nid         notifyIconData
	logWnd      uintptr
	logEdit     uintptr
	logFont     uintptr
	iconAdded   bool
	lastLogText string
	logStickEnd bool
}

type scrollInfo struct {
	CbSize    uint32
	FMask     uint32
	NMin      int32
	NMax      int32
	NPage     uint32
	NPos      int32
	NTrackPos int32
}

func maybeHandleWindowsTrayMode(opts runtimeOptions) (bool, error) {
	if !shouldAutoTrayMode(opts) {
		return false, nil
	}

	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName == "" {
		serviceName = "antgpu-connect"
	}

	stateDir := strings.TrimSpace(opts.StateDir)
	if stateDir == "" {
		stateDir = defaultWindowsStateDir()
	}
	app := &windowsTrayApp{
		serviceName: serviceName,
		displayName: "Antgpu Connect",
		stateDir:    stateDir,
		showLogs:    true,
	}
	return true, app.run()
}

func enableWindowsDPIAwareness() {
	if err := procSetProcessDpiAwarenessCtx.Find(); err == nil {
		if r, _, _ := procSetProcessDpiAwarenessCtx.Call(dpiAwarenessContextPerMonitorAwareV2); r != 0 {
			return
		}
	}
	if err := procSetProcessDPIAware.Find(); err == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

func windowsDPIForWindow(hwnd uintptr) uintptr {
	if hwnd != 0 {
		if err := procGetDpiForWindow.Find(); err == nil {
			if r, _, _ := procGetDpiForWindow.Call(hwnd); r > 0 {
				return r
			}
		}
	}
	if err := procGetDpiForSystem.Find(); err == nil {
		if r, _, _ := procGetDpiForSystem.Call(); r > 0 {
			return r
		}
	}
	return 96
}

func windowsScaleForDPI(value int32, dpi uintptr) int32 {
	if dpi == 0 {
		dpi = 96
	}
	return int32((int64(value)*int64(dpi) + 48) / 96)
}

func windowsSmallIconSize() int32 {
	if err := procGetSystemMetricsForDpi.Find(); err == nil {
		dpi := windowsDPIForWindow(0)
		cx, _, _ := procGetSystemMetricsForDpi.Call(smCxSmallIcon, dpi)
		cy, _, _ := procGetSystemMetricsForDpi.Call(smCySmallIcon, dpi)
		if cx > 0 && cy > 0 {
			if cy < cx {
				cx = cy
			}
			return int32(cx)
		}
	}

	cx, _, _ := procGetSystemMetrics.Call(smCxSmallIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCySmallIcon)
	if cx <= 0 || cy <= 0 {
		return 16
	}
	if cy < cx {
		cx = cy
	}
	return int32(cx)
}

func shouldAutoTrayMode(opts runtimeOptions) bool {
	if strings.TrimSpace(opts.Token) != "" {
		return false
	}
	if opts.Foreground || opts.PrintVersion {
		return false
	}
	if opts.ServiceInstall || opts.ServiceUninstall || opts.ServiceStart || opts.ServiceStop || opts.ServiceRun {
		return false
	}
	return true
}

func (a *windowsTrayApp) run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	enableWindowsDPIAwareness()
	hideConsoleWindow()
	if trayWndProcCallback == 0 {
		trayWndProcCallback = syscall.NewCallback(trayWndProc)
	}

	trayIconSize := windowsSmallIconSize()
	if icon, err := loadEmbeddedTrayIcon(trayIconSize); err == nil {
		a.icon = icon
	} else {
		fmt.Printf("warning: load embedded tray icon: %v\n", err)
	}
	defer destroyIconHandle(a.icon)

	mutex, ok, err := acquireTrayMutex(a.serviceName)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	a.mutex = mutex
	defer func() {
		if a.mutex != 0 {
			_ = windows.CloseHandle(a.mutex)
		}
	}()

	if err := a.createWindow(); err != nil {
		return err
	}
	defer func() {
		if a.hwnd != 0 {
			_, _, _ = procDestroyWindow.Call(a.hwnd)
		}
	}()

	if err := a.createMenu(); err != nil {
		return err
	}
	defer func() {
		if a.menu != 0 {
			_, _, _ = procDestroyMenu.Call(a.menu)
		}
	}()

	if err := a.addTrayIcon(); err != nil {
		return err
	}
	defer func() {
		_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&a.nid)))
	}()

	trayMu.Lock()
	trayInst = a
	trayMu.Unlock()
	defer func() {
		trayMu.Lock()
		trayInst = nil
		trayMu.Unlock()
	}()

	a.refreshStatus()
	a.tryAutoConnectOnLaunch()
	a.refreshStatus()
	if a.showLogs {
		_ = a.openLogViewer(true)
	}
	_, _, _ = procSetTimer.Call(a.hwnd, trayStatusTimerID, 3000, 0)
	defer func() { _, _, _ = procKillTimer.Call(a.hwnd, trayStatusTimerID) }()

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			return fmt.Errorf("GetMessageW failed")
		}
		if r == 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

func (a *windowsTrayApp) tryAutoConnectOnLaunch() {
	status, running := queryWindowsServiceStatus(a.serviceName)
	if running {
		return
	}
	if status != "Stopped" {
		return
	}
	_ = runSelfElevated([]string{"--service-start", "--service-name", a.serviceName, "--state-dir", a.stateDir})
}

func acquireTrayMutex(serviceName string) (windows.Handle, bool, error) {
	// Keep tray singleton per interactive session. Global scope can block
	// user-session tray if a headless/session-0 process already owns mutex.
	name := "Local\\AntgpuConnectTray-" + sanitizeMutexPart(serviceName)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, false, err
	}

	if h, openErr := windows.OpenMutex(windows.SYNCHRONIZE, false, namePtr); openErr == nil {
		_ = windows.CloseHandle(h)
		return 0, false, nil
	} else if !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) {
		return 0, false, openErr
	}

	h, err := windows.CreateMutex(nil, false, namePtr)
	if err != nil {
		return 0, false, err
	}
	return h, true, nil
}

func sanitizeMutexPart(in string) string {
	in = strings.TrimSpace(in)
	if in == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range in {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func hideConsoleWindow() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swHide)
	}
}

func (a *windowsTrayApp) createWindow() error {
	className, err := windows.UTF16PtrFromString("AntgpuConnectTrayWindow")
	if err != nil {
		return err
	}

	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   trayWndProcCallback,
		LpszClassName: className,
		HIcon:         a.icon,
		HIconSm:       a.icon,
	}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		return fmt.Errorf("RegisterClassExW: %v", callErr)
	}

	hwnd, _, createErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", createErr)
	}
	a.hwnd = hwnd
	return nil
}

func (a *windowsTrayApp) createMenu() error {
	menu, _, err := procCreatePopupMenu.Call()
	if menu == 0 {
		return fmt.Errorf("CreatePopupMenu: %v", err)
	}
	a.menu = menu

	if ok, _, appendErr := procAppendMenuW.Call(a.menu, mfString|mfDisabled|mfGray, trayItemStatus, uintptr(unsafe.Pointer(utf16Ptr("Status: checking...")))); ok == 0 {
		return fmt.Errorf("AppendMenuW(status): %v", appendErr)
	}
	if ok, _, appendErr := procAppendMenuW.Call(a.menu, mfSeparator, 0, 0); ok == 0 {
		return fmt.Errorf("AppendMenuW(separator): %v", appendErr)
	}
	if ok, _, appendErr := procAppendMenuW.Call(a.menu, mfString, trayItemLogs, uintptr(unsafe.Pointer(utf16Ptr("View Logs")))); ok == 0 {
		return fmt.Errorf("AppendMenuW(logs): %v", appendErr)
	}
	if ok, _, appendErr := procAppendMenuW.Call(a.menu, mfSeparator, 0, 0); ok == 0 {
		return fmt.Errorf("AppendMenuW(separator2): %v", appendErr)
	}
	if ok, _, appendErr := procAppendMenuW.Call(a.menu, mfString, trayItemAction, uintptr(unsafe.Pointer(utf16Ptr("Disconnect and Exit")))); ok == 0 {
		return fmt.Errorf("AppendMenuW(action): %v", appendErr)
	}

	return nil
}

func (a *windowsTrayApp) addTrayIcon() error {
	icon := a.icon
	if icon == 0 {
		icon, _, _ = procLoadIconW.Call(0, uintptr(32512))
	}

	a.nid.CbSize = uint32(unsafe.Sizeof(a.nid))
	a.nid.HWnd = a.hwnd
	a.nid.UID = 1
	a.nid.UFlags = nifMessage | nifIcon | nifTip
	a.nid.UCallbackMessage = wmTrayCallback
	a.nid.HIcon = icon
	copy(a.nid.SzTip[:], []uint16{'A', 'n', 't', 'h', 'i', 'v', 'e', ' ', 'C', 'o', 'n', 'n', 'e', 'c', 't', 0})

	r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&a.nid)))
	if r == 0 {
		return fmt.Errorf("Shell_NotifyIconW(NIM_ADD): %v", err)
	}
	return nil
}

func (a *windowsTrayApp) refreshStatus() {
	status, running := queryWindowsServiceStatus(a.serviceName)
	_, _, _ = procModifyMenuW.Call(a.menu, trayItemStatus, mfByCommand|mfString|mfDisabled|mfGray, trayItemStatus, uintptr(unsafe.Pointer(utf16Ptr("Status: "+status))))

	if running {
		_, _, _ = procModifyMenuW.Call(a.menu, trayItemAction, mfByCommand|mfString|mfEnabled, trayItemAction, uintptr(unsafe.Pointer(utf16Ptr("Disconnect and Exit"))))
		_, _, _ = procEnableMenuItem.Call(a.menu, trayItemAction, mfByCommand|mfEnabled)
	} else {
		_, _, _ = procModifyMenuW.Call(a.menu, trayItemAction, mfByCommand|mfString|mfEnabled, trayItemAction, uintptr(unsafe.Pointer(utf16Ptr("Exit"))))
		_, _, _ = procEnableMenuItem.Call(a.menu, trayItemAction, mfByCommand|mfEnabled)
	}

	setNotifyIconTooltip(&a.nid, "Antgpu Connect - "+status)
	_, _, _ = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&a.nid)))
}

func (a *windowsTrayApp) showMenu() {
	a.refreshStatus()

	var p point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	_, _, _ = procSetForegroundWindow.Call(a.hwnd)

	cmd, _, _ := procTrackPopupMenu.Call(a.menu, tpmLeftAlign|tpmBottomAlign|tpmRightButton|tpmReturnCmd|tpmNoNotify, uintptr(p.X), uintptr(p.Y), 0, a.hwnd, 0)

	switch cmd {
	case trayItemLogs:
		_ = a.openLogViewer(true)
	case trayItemAction:
		if _, running := queryWindowsServiceStatus(a.serviceName); running {
			_ = runSelfElevated([]string{"--service-stop", "--service-name", a.serviceName, "--state-dir", a.stateDir})
		}
		_, _, _ = procDestroyWindow.Call(a.hwnd)
	}

	_, _, _ = procPostMessageW.Call(a.hwnd, 0, 0, 0)
}

func (a *windowsTrayApp) openLogViewer(focus bool) error {
	if a.logWnd != 0 {
		a.refreshLogViewer()
		if focus {
			_, _, _ = procShowWindow.Call(a.logWnd, swShow)
			_, _, _ = procSetForegroundWindow.Call(a.logWnd)
		}
		return nil
	}

	className := utf16Ptr("AntgpuConnectLogsWindow")
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   trayWndProcCallback,
		LpszClassName: className,
		HIcon:         a.icon,
		HIconSm:       a.icon,
	}
	atom, _, classErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		if !errors.Is(classErr, syscall.Errno(1410)) { // ERROR_CLASS_ALREADY_EXISTS
			return fmt.Errorf("RegisterClassExW(logs): %v", classErr)
		}
	}

	dpi := windowsDPIForWindow(0)
	title := utf16Ptr("Safe to Close - " + a.displayName + " Logs")
	wnd, _, createErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsVisible,
		uintptr(windowsScaleForDPI(120, dpi)),
		uintptr(windowsScaleForDPI(120, dpi)),
		uintptr(windowsScaleForDPI(980, dpi)),
		uintptr(windowsScaleForDPI(620, dpi)),
		0,
		0,
		0,
		0,
	)
	if wnd == 0 {
		return fmt.Errorf("CreateWindowExW(logs): %v", createErr)
	}
	a.logWnd = wnd

	editClass := utf16Ptr("EDIT")
	edit, _, editErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(utf16Ptr(""))),
		wsChild|wsVisible|wsVScroll|wsHScroll|esMultiline|esAutoVScroll|esAutoHScroll|esReadOnly,
		uintptr(windowsScaleForDPI(8, dpi)),
		uintptr(windowsScaleForDPI(8, dpi)),
		uintptr(windowsScaleForDPI(940, dpi)),
		uintptr(windowsScaleForDPI(560, dpi)),
		wnd,
		0,
		0,
		0,
	)
	if edit == 0 {
		_, _, _ = procDestroyWindow.Call(wnd)
		a.logWnd = 0
		return fmt.Errorf("CreateWindowExW(log edit): %v", editErr)
	}
	a.logEdit = edit
	a.applyLogFont()
	a.lastLogText = ""
	a.logStickEnd = true
	a.layoutLogEdit()
	a.refreshLogViewer()
	_, _, _ = procSetTimer.Call(a.hwnd, logRefreshTimerID, 2000, 0)
	if focus {
		_, _, _ = procShowWindow.Call(a.logWnd, swShowNormal)
		_, _, _ = procSetForegroundWindow.Call(a.logWnd)
	}
	return nil
}

func (a *windowsTrayApp) closeLogViewer() {
	if a.logWnd == 0 {
		return
	}
	_, _, _ = procDestroyWindow.Call(a.logWnd)
	a.logWnd = 0
	a.logEdit = 0
	if a.logFont != 0 {
		_, _, _ = procDeleteObject.Call(a.logFont)
		a.logFont = 0
	}
	a.lastLogText = ""
	a.logStickEnd = true
	_, _, _ = procKillTimer.Call(a.hwnd, logRefreshTimerID)
}

func (a *windowsTrayApp) applyLogFont() {
	if a.logEdit == 0 {
		return
	}
	dpi := windowsDPIForWindow(a.logWnd)
	height := -int32((9*int(dpi) + 36) / 72)
	font, _, _ := procCreateFontW.Call(
		uintptr(uint32(height)),
		0,
		0,
		0,
		400,
		0,
		0,
		0,
		1,
		0,
		0,
		5,
		0,
		uintptr(unsafe.Pointer(utf16Ptr("Consolas"))),
	)
	if font == 0 {
		return
	}
	if a.logFont != 0 {
		_, _, _ = procDeleteObject.Call(a.logFont)
	}
	a.logFont = font
	_, _, _ = procSendMessageW.Call(a.logEdit, wmSetFont, font, 1)
}

func (a *windowsTrayApp) layoutLogEdit() {
	if a.logWnd == 0 || a.logEdit == 0 {
		return
	}
	var rc rect
	if r, _, _ := procGetClientRect.Call(a.logWnd, uintptr(unsafe.Pointer(&rc))); r == 0 {
		return
	}
	margin := windowsScaleForDPI(8, windowsDPIForWindow(a.logWnd))
	padding := margin * 2
	width := int(rc.Right - rc.Left - padding)
	height := int(rc.Bottom - rc.Top - padding)
	if width < 10 {
		width = 10
	}
	if height < 10 {
		height = 10
	}
	_, _, _ = procMoveWindow.Call(a.logEdit, uintptr(margin), uintptr(margin), uintptr(width), uintptr(height), 1)
}

func (a *windowsTrayApp) refreshLogViewer() {
	if a.logEdit == 0 {
		return
	}
	if isEditAtBottom(a.logEdit) {
		a.logStickEnd = true
	} else if !hasEditSelection(a.logEdit) {
		a.logStickEnd = false
	}

	content := a.readLogSnapshot()
	if content == a.lastLogText {
		return
	}
	if hasEditSelection(a.logEdit) {
		// Keep user selection stable while they inspect/copy text.
		return
	}

	prevFirst, _ := getEditFirstVisibleLine(a.logEdit)
	stickToEnd := a.logStickEnd
	_, _, _ = procSetWindowTextW.Call(a.logEdit, uintptr(unsafe.Pointer(utf16Ptr(content))))
	a.lastLogText = content

	if stickToEnd {
		scrollEditToBottom(a.logEdit)
		a.logStickEnd = true
		return
	}

	curFirst, ok := getEditFirstVisibleLine(a.logEdit)
	if !ok {
		return
	}
	delta := prevFirst - curFirst
	if delta != 0 {
		_, _, _ = procSendMessageW.Call(a.logEdit, emLineScroll, 0, uintptr(int64(delta)))
	}
}

func scrollEditToBottom(edit uintptr) {
	_, _, _ = procSendMessageW.Call(edit, emSetSel, ^uintptr(0), ^uintptr(0))
	_, _, _ = procSendMessageW.Call(edit, emScrollCaret, 0, 0)
	_, _, _ = procSendMessageW.Call(edit, wmVScroll, sbBottom, 0)
}

func hasEditSelection(edit uintptr) bool {
	var start uint32
	var end uint32
	_, _, _ = procSendMessageW.Call(edit, emGetSel, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	return start != end
}

func getEditFirstVisibleLine(edit uintptr) (int, bool) {
	r, _, _ := procSendMessageW.Call(edit, emGetFirstVisLine, 0, 0)
	if int32(r) < 0 {
		return 0, false
	}
	return int(int32(r)), true
}

func isEditAtBottom(edit uintptr) bool {
	const (
		sbVert = 1
		sifAll = 0x0017
	)
	var si scrollInfo
	si.CbSize = uint32(unsafe.Sizeof(si))
	si.FMask = sifAll
	r, _, _ := procGetScrollInfo.Call(edit, sbVert, uintptr(unsafe.Pointer(&si)))
	if r == 0 {
		return true
	}

	maxTop := si.NMax - int32(si.NPage) + 1
	if maxTop < si.NMin {
		maxTop = si.NMin
	}
	return si.NPos >= maxTop-1
}

func (a *windowsTrayApp) readLogSnapshot() string {
	candidates := []string{
		filepath.Join(a.stateDir, "connect.log"),
	}

	var b strings.Builder
	for _, path := range candidates {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := readTailFile(path, 250_000)
		if err != nil {
			continue
		}
		if data == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\r\n\r\n")
		}
		b.WriteString("===== ")
		b.WriteString(path)
		b.WriteString(" =====\r\n")
		b.WriteString(data)
	}

	if b.Len() == 0 {
		return "No logs found yet in state directory.\r\nExpected file:\r\n - " +
			filepath.Join(a.stateDir, "connect.log")
	}
	return b.String()
}

func readTailFile(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	if size <= 0 {
		return "", nil
	}

	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if start > 0 {
		if idx := strings.IndexByte(string(buf), '\n'); idx >= 0 && idx+1 < len(buf) {
			buf = buf[idx+1:]
		}
	}
	return strings.ReplaceAll(string(buf), "\n", "\r\n"), nil
}

func trayWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	trayMu.Lock()
	a := trayInst
	trayMu.Unlock()

	if a != nil && hwnd == a.logWnd {
		switch msg {
		case wmSize:
			a.layoutLogEdit()
			return 0
		case wmDpiChanged:
			if lParam != 0 {
				var r rect
				// Window messages carry an external address. Let Windows validate
				// it before copying, rather than dereferencing a uintptr in Go.
				if err := windows.ReadProcessMemory(windows.CurrentProcess(), lParam, (*byte)(unsafe.Pointer(&r)), unsafe.Sizeof(r), nil); err == nil {
					_, _, _ = procMoveWindow.Call(
						hwnd,
						uintptr(uint32(r.Left)),
						uintptr(uint32(r.Top)),
						uintptr(uint32(r.Right-r.Left)),
						uintptr(uint32(r.Bottom-r.Top)),
						1,
					)
				}
			}
			a.applyLogFont()
			a.layoutLogEdit()
			return 0
		case wmDestroy:
			a.logWnd = 0
			a.logEdit = 0
			_, _, _ = procKillTimer.Call(a.hwnd, logRefreshTimerID)
			return 0
		}
	}

	switch msg {
	case wmTrayCallback:
		if a != nil {
			switch uint32(lParam) {
			case wmRButtonUp, wmLButtonUp, wmContextMenu:
				a.showMenu()
			}
		}
		return 0
	case wmTimer:
		if a != nil {
			switch wParam {
			case trayStatusTimerID:
				a.refreshStatus()
			case logRefreshTimerID:
				a.refreshLogViewer()
			}
		}
		return 0
	case wmCommand:
		// Reserved for future log-window controls.
		return 0
	case wmDestroy:
		if a != nil && hwnd == a.hwnd {
			a.closeLogViewer()
			_, _, _ = procPostQuitMessage.Call(0)
		}
		return 0
	default:
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	}
}

func runSelfElevated(args []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		exePath, _ = os.Executable()
	}

	r, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr("runas"))),
		uintptr(unsafe.Pointer(utf16Ptr(exePath))),
		uintptr(unsafe.Pointer(utf16Ptr(joinWindowsArgs(args)))),
		0,
		1,
	)
	if int32(r) <= 32 {
		return fmt.Errorf("ShellExecuteW failed with code=%d", r)
	}
	return nil
}

func joinWindowsArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if strings.ContainsAny(arg, " \t\"") {
			arg = strings.ReplaceAll(arg, "\"", "\\\"")
			arg = "\"" + arg + "\""
		}
		quoted = append(quoted, arg)
	}
	return strings.Join(quoted, " ")
}

func queryWindowsServiceStatus(serviceName string) (string, bool) {
	mgrHandle, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "Unavailable", false
	}
	defer windows.CloseServiceHandle(mgrHandle)

	svcNamePtr, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return "Unavailable", false
	}
	svcHandle, err := windows.OpenService(mgrHandle, svcNamePtr, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST):
			return "Not Installed", false
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return "Access Denied", false
		default:
			return "Unavailable", false
		}
	}
	defer windows.CloseServiceHandle(svcHandle)

	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(svcHandle, &st); err != nil {
		return "Unavailable", false
	}

	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "Running", true
	case windows.SERVICE_START_PENDING:
		return "Starting", false
	case windows.SERVICE_STOP_PENDING:
		return "Stopping", false
	case windows.SERVICE_STOPPED:
		return "Stopped", false
	default:
		return "Unknown", false
	}
}

func setNotifyIconTooltip(nid *notifyIconData, tooltip string) {
	for i := range nid.SzTip {
		nid.SzTip[i] = 0
	}
	runes, _ := windows.UTF16FromString(tooltip)
	if len(runes) == 0 {
		return
	}
	max := len(nid.SzTip)
	if len(runes) < max {
		max = len(runes)
	}
	copy(nid.SzTip[:max], runes[:max])
	nid.UFlags = nifMessage | nifIcon | nifTip
}

func utf16Ptr(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}
