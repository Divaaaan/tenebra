//go:build windows

package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	inetSettingsKey               = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	proxyLeaseKey                 = `Software\Tenebra`
	proxyLeaseValue               = "SystemProxyLease"
	proxyHelperFlag               = "--user-proxy-helper"
	proxyLeaseMaxBytes            = 64 << 10
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
	internetOptionPerConnection   = 75
)

var (
	modWininet              = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption   = modWininet.NewProc("InternetSetOptionW")
	procInternetQueryOption = modWininet.NewProc("InternetQueryOptionW")
	procProxyGlobalFree     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
	procProxyRegFlushKey    = windows.NewLazySystemDLL("advapi32.dll").NewProc("RegFlushKey")
)

func newSystemProxyController() systemProxyController {
	return &sessionSystemProxy{ops: windowsProxySessions{}}
}

type windowsProxySessions struct{}

func (windowsProxySessions) Current() (proxyUser, error) {
	self, err := currentUserSID()
	if err != nil {
		return proxyUser{}, err
	}
	if self != "S-1-5-18" {
		var session uint32
		if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
			return proxyUser{}, err
		}
		return proxyUser{SID: self, Session: session}, nil
	}
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xffffffff {
		return proxyUser{}, errors.New("no active console user for system proxy")
	}
	tok, err := proxySessionToken(proxyUser{Session: session})
	if err != nil {
		return proxyUser{}, err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return proxyUser{}, err
	}
	return proxyUser{SID: u.User.Sid.String(), Session: session}, nil
}

// A reused session ID must never restore a lease into another user's account.
func proxySessionToken(u proxyUser) (windows.Token, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(u.Session, &tok); err != nil {
		return 0, fmt.Errorf("open interactive user token: %w", err)
	}
	tu, err := tok.GetTokenUser()
	if err != nil || (u.SID != "" && tu.User.Sid.String() != u.SID) {
		tok.Close()
		return 0, errors.New("system proxy owner session is unavailable or changed")
	}
	return tok, nil
}

func (windowsProxySessions) Run(u proxyUser, action, target string) error {
	self, err := currentUserSID()
	if err != nil {
		return err
	}
	if self != "S-1-5-18" {
		if self != u.SID {
			return errors.New("system proxy owner differs from the current user")
		}
		return runUserProxyAction(action, target)
	}
	tok, err := proxySessionToken(u)
	if err != nil {
		return err
	}
	defer tok.Close()
	return launchUserProxyHelper(tok, action, target)
}

func openProxyUserKey(u proxyUser, path string) (registry.Key, error) {
	if u.SID == "" {
		return 0, errors.New("missing system proxy owner")
	}
	return registry.OpenKey(registry.USERS, u.SID+`\`+path, registry.QUERY_VALUE)
}

func (windowsProxySessions) Read(u proxyUser) (proxyState, error) {
	k, err := openProxyUserKey(u, inetSettingsKey)
	if err != nil {
		return proxyState{}, err
	}
	defer k.Close()
	on, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return proxyState{}, err
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return proxyState{}, err
	}
	return proxyState{Enabled: on == 1, Server: firstProxyTarget(server)}, nil
}

func (windowsProxySessions) HasLease(u proxyUser) (bool, error) {
	k, err := openProxyUserKey(u, proxyLeaseKey)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetValue(proxyLeaseValue, nil)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// WinINet is unsupported in services. Run the installed, administrator-protected
// core as the interactive user, before any normal daemon initialization.
// No inherited handles cross the session boundary.
// https://learn.microsoft.com/en-us/windows/win32/wininet/enabling-internet-functionality
func launchUserProxyHelper(tok windows.Token, action, target string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{exe, proxyHelperFlag, action}
	if action == "apply" {
		args = append(args, target)
	}
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cmd, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		return fmt.Errorf("create user environment: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, app, cmd, nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, dir, &si, &pi); err != nil {
		return fmt.Errorf("start interactive user proxy helper: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)
	wait, err := windows.WaitForSingleObject(pi.Process, 12_000)
	if err != nil || wait != windows.WAIT_OBJECT_0 {
		// Only this helper is terminated. The durable snapshot survives timeout;
		// the daemon retains cleanup ownership and retries restore.
		_ = windows.TerminateProcess(pi.Process, 1)
		_, _ = windows.WaitForSingleObject(pi.Process, 1_000)
		return errors.New("interactive user proxy helper timed out or could not be waited for; restore remains pending")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("interactive user proxy %s failed (exit %d)", action, code)
	}
	return nil
}

// RunUserProxyHelper recognizes a tiny protocol before flag parsing/service
// detection. It can never launch an engine or background jobs.
func RunUserProxyHelper(args []string) (bool, error) {
	if len(args) == 0 || args[0] != proxyHelperFlag {
		return false, nil
	}
	if len(args) == 2 && args[1] == "restore" {
		return true, runUserProxyAction("restore", "")
	}
	if len(args) == 3 && args[1] == "apply" && validUserProxyTarget(args[2]) {
		return true, runUserProxyAction("apply", args[2])
	}
	return true, errors.New("invalid user proxy helper arguments")
}

func runUserProxyAction(action, target string) error {
	if action != "restore" && (action != "apply" || !validUserProxyTarget(target)) {
		return errors.New("invalid user proxy operation")
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	if sid == "S-1-5-18" || sid == "S-1-5-19" || sid == "S-1-5-20" {
		return errors.New("WinINet proxy helper requires an interactive user")
	}
	release, err := acquireUserProxyLock(sid)
	if err != nil {
		return err
	}
	defer release()
	ops := wininetProxyOperations{}
	if action == "apply" {
		return applyUserProxy(ops, target)
	}
	return restoreUserProxy(ops)
}

type wininetProxyOperations struct{}

func (wininetProxyOperations) Load() (*userProxyLease, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, proxyLeaseKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer k.Close()
	buf := make([]byte, proxyLeaseMaxBytes)
	n, kind, err := k.GetValue(proxyLeaseValue, buf)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if kind != registry.BINARY || n > len(buf) {
		return nil, errors.New("invalid user proxy snapshot format")
	}
	var lease userProxyLease
	if err := json.Unmarshal(buf[:n], &lease); err != nil {
		return nil, errors.New("invalid user proxy snapshot JSON")
	}
	return &lease, nil
}

func (wininetProxyOperations) Save(lease userProxyLease) error {
	buf, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	if len(buf) > proxyLeaseMaxBytes {
		return errors.New("user proxy snapshot is too large")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, proxyLeaseKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetBinaryValue(proxyLeaseValue, buf); err != nil {
		return err
	}
	return flushProxyJournal(k)
}

func (wininetProxyOperations) Delete() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, proxyLeaseKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(proxyLeaseValue); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return flushProxyJournal(k)
}

func flushProxyJournal(k registry.Key) error {
	code, _, _ := procProxyRegFlushKey.Call(uintptr(k))
	if code != 0 {
		return fmt.Errorf("flush user proxy snapshot: %w", windows.Errno(code))
	}
	return nil
}

// INTERNET_PER_CONN_OPTION's union is eight bytes (FILETIME), aligned as a
// pointer on 64-bit Windows and as a DWORD on 32-bit Windows.
type internetPerConnOption struct {
	Option uint32
	Value  uint64
}
type internetPerConnList struct {
	Size       uint32
	Connection *uint16
	Count      uint32
	Error      uint32
	Options    *internetPerConnOption
}

func proxyOptionString(o *internetPerConnOption) *uint16 {
	return *(**uint16)(unsafe.Pointer(&o.Value))
}

func freeProxyOptionStrings(opts []internetPerConnOption) {
	for i := 1; i < len(opts); i++ {
		if p := proxyOptionString(&opts[i]); p != nil {
			procProxyGlobalFree.Call(uintptr(unsafe.Pointer(p)))
			opts[i].Value = 0
		}
	}
}

func (wininetProxyOperations) Read() (userProxySettings, error) {
	// Query FLAGS_UI (10) with FLAGS (1) fallback; write with FLAGS (1).
	for _, flagOption := range []uint32{10, 1} {
		opts := []internetPerConnOption{{Option: flagOption}, {Option: 2}, {Option: 3}, {Option: 4}}
		list := internetPerConnList{Count: uint32(len(opts)), Options: &opts[0]}
		list.Size = uint32(unsafe.Sizeof(list))
		size := list.Size
		r, _, err := procInternetQueryOption.Call(0, internetOptionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
		if r == 0 {
			freeProxyOptionStrings(opts)
			if flagOption == 10 {
				continue
			}
			return userProxySettings{}, fmt.Errorf("query user WinINet proxy: %w", err)
		}
		st := userProxySettings{Flags: uint32(opts[0].Value), Server: windows.UTF16PtrToString(proxyOptionString(&opts[1])), Bypass: windows.UTF16PtrToString(proxyOptionString(&opts[2])), PAC: windows.UTF16PtrToString(proxyOptionString(&opts[3]))}
		freeProxyOptionStrings(opts)
		return st, nil
	}
	return userProxySettings{}, errors.New("user proxy query unavailable")
}

func (wininetProxyOperations) Write(st userProxySettings) error {
	opts := []internetPerConnOption{{Option: 1, Value: uint64(st.Flags)}, {Option: 2}, {Option: 3}, {Option: 4}}
	keep := make([]*uint16, 0, 3)
	for i, s := range []string{st.Server, st.Bypass, st.PAC} {
		ptr, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return err
		}
		keep = append(keep, ptr)
		*(*unsafe.Pointer)(unsafe.Pointer(&opts[i+1].Value)) = unsafe.Pointer(ptr)
	}
	list := internetPerConnList{Count: uint32(len(opts)), Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	r, _, err := procInternetSetOption.Call(0, internetOptionPerConnection, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(keep)
	if r == 0 {
		return fmt.Errorf("set user WinINet proxy: %w", err)
	}
	return refreshWinINet()
}

func firstProxyTarget(v string) string {
	first := strings.TrimSpace(v)
	if i := strings.IndexByte(first, ';'); i >= 0 {
		first = first[:i]
	}
	if i := strings.IndexByte(first, '='); i >= 0 {
		first = first[i+1:]
	}
	return strings.TrimSpace(first)
}

func refreshWinINet() error {
	if r, _, err := procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0); r == 0 {
		return fmt.Errorf("InternetSetOption(SETTINGS_CHANGED): %w", err)
	}
	if r, _, err := procInternetSetOption.Call(0, internetOptionRefresh, 0, 0); r == 0 {
		return fmt.Errorf("InternetSetOption(REFRESH): %w", err)
	}
	return nil
}
