//go:build windows

package localproc

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")
var iphelper = syscall.NewLazyDLL("iphlpapi.dll")
var advapi = syscall.NewLazyDLL("advapi32.dll")
var openProcess = kernel.NewProc("OpenProcess")
var getProcessTimes = kernel.NewProc("GetProcessTimes")
var queryImage = kernel.NewProc("QueryFullProcessImageNameW")
var closeHandle = kernel.NewProc("CloseHandle")
var tcpTable = iphelper.NewProc("GetExtendedTcpTable")
var attachConsole = kernel.NewProc("AttachConsole")
var freeConsole = kernel.NewProc("FreeConsole")
var consoleProcesses = kernel.NewProc("GetConsoleProcessList")
var consoleHandler = kernel.NewProc("SetConsoleCtrlHandler")
var consoleEvent = kernel.NewProc("GenerateConsoleCtrlEvent")
var convertSD = advapi.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
var setFileSecurity = advapi.NewProc("SetFileSecurityW")
var localFree = kernel.NewProc("LocalFree")

func winerr(op string, err error) error { return fmt.Errorf("%s: %w", op, err) }
func Start(o StartOptions) (Identity, error) {
	exe, err := filepath.Abs(o.Executable)
	if err != nil {
		return Identity{}, err
	}
	cmd := exec.Command(exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if o.Console {
		cmd.SysProcAttr.CreationFlags = 0x00000010
	}
	if err = cmd.Start(); err != nil {
		return Identity{}, err
	}
	id, err := Inspect(cmd.Process.Pid)
	if err == nil {
		err = Track(id)
	}
	_ = cmd.Process.Release()
	return id, err
}
func Inspect(pid int) (Identity, error) {
	if pid <= 0 {
		return Identity{}, errors.New("invalid process ID")
	}
	h, _, e := openProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return Identity{}, winerr("OpenProcess", e)
	}
	defer closeHandle.Call(h)
	var created, exit, kernelTime, userTime syscall.Filetime
	ok, _, e := getProcessTimes.Call(h, uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime)))
	if ok == 0 {
		return Identity{}, winerr("GetProcessTimes", e)
	}
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return Identity{}, errors.New("process has exited")
	}
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	ok, _, e = queryImage.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if ok == 0 {
		return Identity{}, winerr("QueryFullProcessImageName", e)
	}
	return Identity{PID: pid, Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), Executable: syscall.UTF16ToString(buf[:n])}, nil
}

// Gone requires positive evidence that the original process has ended. Unlike
// !Matches, denied or failed identity queries are not treated as proof of exit.
func Gone(id Identity) (bool, error) {
	if id.PID <= 0 || id.Created == 0 || id.Executable == "" {
		return false, errors.New("invalid process identity")
	}
	if exited, _, known := ExitStatus(id); known && exited {
		return true, nil
	}
	h, _, e := openProcess.Call(0x100000|0x1000, 0, uintptr(id.PID))
	if h == 0 {
		// Windows reports ERROR_INVALID_PARAMETER for a PID that no longer exists.
		if e == syscall.Errno(87) {
			return true, nil
		}
		return false, winerr("OpenProcess", e)
	}
	defer closeHandle.Call(h)
	status, _, e := waitSingle.Call(h, 0)
	if status == 0 {
		return true, nil
	}
	if status != 258 {
		return false, winerr("WaitForSingleObject", e)
	}
	var created, exited, kernelTime, userTime syscall.Filetime
	ok, _, e := getProcessTimes.Call(h, uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime)))
	if ok == 0 {
		return false, winerr("GetProcessTimes", e)
	}
	actualCreated := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return actualCreated != id.Created || exited.HighDateTime != 0 || exited.LowDateTime != 0, nil
}

func Listeners(port int) ([]Identity, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid port")
	}
	seen := map[int]bool{}
	var result []Identity
	for _, family := range []uintptr{2, 23} {
		var size uint32
		code, _, _ := tcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, family, 3, 0)
		if code != 122 {
			return nil, fmt.Errorf("GetExtendedTcpTable size: %d", code)
		}
		buf := make([]byte, size)
		code, _, _ = tcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, family, 3, 0)
		if code != 0 {
			return nil, fmt.Errorf("GetExtendedTcpTable: %d", code)
		}
		row, portOffset, pidOffset := 24, 8, 20
		if family == 23 {
			row, portOffset, pidOffset = 56, 20, 52
		}
		count := int(binary.LittleEndian.Uint32(buf))
		if count > (len(buf)-4)/row {
			return nil, errors.New("invalid TCP table")
		}
		for i := 0; i < count; i++ {
			b := buf[4+i*row : 4+(i+1)*row]
			if int(binary.BigEndian.Uint16(b[portOffset:])) != port {
				continue
			}
			pid := int(binary.LittleEndian.Uint32(b[pidOffset:]))
			if seen[pid] {
				continue
			}
			id, err := Inspect(pid)
			if err != nil {
				return nil, err
			}
			seen[pid] = true
			result = append(result, id)
		}
	}
	return result, nil
}
func stopConsole(ctx context.Context, id Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !Matches(id) {
		return errors.New("process identity no longer matches")
	}
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-console-stop", base64.RawURLEncoding.EncodeToString(data))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		exited, code, known := ExitStatus(id)
		if !known {
			return errors.New("target exit status unavailable")
		}
		if exited {
			if code != 0 {
				return fmt.Errorf("target exited with code %d", code)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func ConsoleHelper(encoded string) error {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	var id Identity
	if err = json.Unmarshal(data, &id); err != nil {
		return err
	}
	if !Matches(id) {
		return errors.New("process identity no longer matches")
	}
	// The helper begins without a console and attaches only to the identified target.
	freeConsole.Call()
	ok, _, e := attachConsole.Call(uintptr(id.PID))
	if ok == 0 {
		return winerr("AttachConsole", e)
	}
	var pids [3]uint32
	count, _, e := consoleProcesses.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if count != 2 {
		return fmt.Errorf("refusing shared or unknown console (members %d): %v", count, e)
	}
	a, b := uint32(id.PID), uint32(os.Getpid())
	if !((pids[0] == a && pids[1] == b) || (pids[0] == b && pids[1] == a)) || !Matches(id) {
		return errors.New("console membership or identity changed")
	}
	// This helper is a transient process: keep Ctrl+C ignored and remain attached
	// until it exits. Restoring the handler or freeing the console here races
	// delivery of the event to the helper itself.
	ok, _, e = consoleHandler.Call(0, 1)
	if ok == 0 {
		return winerr("SetConsoleCtrlHandler", e)
	}
	count, _, e = consoleProcesses.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if count != 2 || !((pids[0] == a && pids[1] == b) || (pids[0] == b && pids[1] == a)) || !Matches(id) {
		return errors.New("console membership or identity changed")
	}
	ok, _, e = consoleEvent.Call(0, 0)
	if ok == 0 {
		return winerr("GenerateConsoleCtrlEvent", e)
	}
	return nil
}
func SecureDir(path string) error {
	// Install the protected DACL at creation, so a newly created directory is
	// never briefly exposed under its parent's inherited permissions.
	sddl, _ := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;OW)(A;OICI;FA;;;SY)")
	var sd uintptr
	ok, _, e := convertSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&sd)), 0)
	if ok == 0 {
		return winerr("security descriptor", e)
	}
	defer localFree.Call(sd)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	type securityAttributes struct {
		Length             uint32
		SecurityDescriptor uintptr
		InheritHandle      int32
	}
	sa := securityAttributes{Length: uint32(unsafe.Sizeof(securityAttributes{})), SecurityDescriptor: sd}
	create := kernel.NewProc("CreateDirectoryW")
	ok, _, e = create.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&sa)))
	if ok != 0 {
		return nil
	}
	if e != syscall.ERROR_ALREADY_EXISTS {
		return winerr("CreateDirectory", e)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime path is not a directory")
	}
	ok, _, e = setFileSecurity.Call(uintptr(unsafe.Pointer(p)), 4|0x80000000, sd)
	if ok == 0 {
		return winerr("SetFileSecurity", e)
	}
	return nil
}

var ntdll = syscall.NewLazyDLL("ntdll.dll")
var queryProcess = ntdll.NewProc("NtQueryInformationProcess")

// ProcessArgs reads the native process command line without starting a subprocess.
// Callers must revalidate process identity around this query before trusting it.
func ProcessArgs(pid int) ([]string, error) {
	if pid <= 0 {
		return nil, errors.New("invalid process ID")
	}
	h, _, e := openProcess.Call(0x1000, 0, uintptr(pid))
	if h == 0 {
		return nil, winerr("OpenProcess", e)
	}
	defer closeHandle.Call(h)
	var needed uint32
	queryProcess.Call(h, 60, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 || needed > 1024*1024 {
		return nil, errors.New("invalid process command line size")
	}
	buf := make([]byte, needed)
	status, _, _ := queryProcess.Call(h, 60, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&needed)))
	if status != 0 {
		return nil, fmt.Errorf("NtQueryInformationProcess: status %#x", uint32(status))
	}
	type unicodeString struct {
		Length        uint16
		MaximumLength uint16
		Buffer        *uint16
	}
	if len(buf) < int(unsafe.Sizeof(unicodeString{})) {
		return nil, errors.New("short command line")
	}
	u := (*unicodeString)(unsafe.Pointer(&buf[0]))
	start := uintptr(unsafe.Pointer(u.Buffer))
	base := uintptr(unsafe.Pointer(&buf[0]))
	end := base + uintptr(len(buf))
	if u.Length%2 != 0 || start < base || start > end || uintptr(u.Length) > end-start {
		return nil, errors.New("invalid command line buffer")
	}
	text := syscall.UTF16ToString(unsafe.Slice(u.Buffer, int(u.Length)/2))
	ptr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return nil, err
	}
	var count int32
	argv, err := syscall.CommandLineToArgv(ptr, &count)
	if err != nil {
		return nil, err
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(argv))))
	if count < 0 || count > 32768 {
		return nil, errors.New("invalid argument count")
	}
	pointers := unsafe.Slice((**uint16)(unsafe.Pointer(argv)), int(count))
	args := make([]string, len(pointers))
	for i, p := range pointers {
		if p == nil {
			return nil, errors.New("invalid argument")
		}
		n := 0
		for n < 32768 && *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
			n++
		}
		if n == 32768 {
			return nil, errors.New("unterminated argument")
		}
		args[i] = syscall.UTF16ToString(unsafe.Slice(p, n))
	}
	return args, nil
}

var waitSingle = kernel.NewProc("WaitForSingleObject")
var getExitCode = kernel.NewProc("GetExitCodeProcess")

type trackedProcess struct {
	handle uintptr
	exited bool
	code   uint32
}

var trackedMu sync.Mutex
var tracked = map[Identity]*trackedProcess{}

// Track reopens an identity after launcher recovery, retaining a handle until exit.
func Track(id Identity) error {
	trackedMu.Lock()
	defer trackedMu.Unlock()
	if _, ok := tracked[id]; ok {
		return nil
	}
	if !Matches(id) {
		return errors.New("process identity no longer matches")
	}
	h, _, e := openProcess.Call(0x100000|0x1000, 0, uintptr(id.PID))
	if h == 0 {
		return winerr("OpenProcess", e)
	}
	if !Matches(id) {
		closeHandle.Call(h)
		return errors.New("process identity changed during tracking")
	}
	tracked[id] = &trackedProcess{handle: h}
	return nil
}

// ExitStatus returns known=false when no verified retained handle or result exists.
func ExitStatus(id Identity) (exited bool, code uint32, known bool) {
	trackedMu.Lock()
	defer trackedMu.Unlock()
	p, ok := tracked[id]
	if !ok {
		return false, 0, false
	}
	if p.exited {
		return true, p.code, true
	}
	status, _, _ := waitSingle.Call(p.handle, 0)
	if status == 258 {
		return false, 0, true
	}
	if status != 0 {
		return false, 0, false
	}
	var c uint32
	okValue, _, _ := getExitCode.Call(p.handle, uintptr(unsafe.Pointer(&c)))
	if okValue == 0 {
		return false, 0, false
	}
	p.exited = true
	p.code = c
	closeHandle.Call(p.handle)
	p.handle = 0
	return true, c, true
}
