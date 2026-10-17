package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"github.com/magarcia/expensify-cli/internal/fault"
	"golang.org/x/sys/windows"
)

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func privateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	// Protect the DACL from inherited grants before any credential data is written.
	return windows.SecurityDescriptorFromString("O:" + sid.String() + "D:P(A;" + inheritance + ";FA;;;" + sid.String() + ")")
}

func createPrivateDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	descriptor, err := privateDescriptor(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	err = windows.CreateDirectory(name, &attributes)
	runtime.KeepAlive(descriptor)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil
	}
	return err
}

func setPrivatePermissions(path string, directory bool) error {
	descriptor, err := privateDescriptor(directory)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
	runtime.KeepAlive(descriptor)
	return err
}

func makeFilePrivate(file *os.File) error {
	return setPrivatePermissions(file.Name(), false)
}

func replacePrivateFile(source, target string) error {
	for attempt := range 8 {
		err := os.Rename(source, target)
		temporarilyLocked := errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
		if !temporarilyLocked || attempt == 7 {
			return err
		}
		// Windows readers can temporarily prevent replacement without sharing deletion.
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return nil
}

func checkPrivatePermissions(path string, _ os.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return windowsPermissionError()
	}
	defer runtime.KeepAlive(descriptor)
	current, err := currentUserSID()
	if err != nil {
		return windowsPermissionError()
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(current) {
		return windowsPermissionError()
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return windowsPermissionError()
	}
	userAccess := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return windowsPermissionError()
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if sid.Equals(current) {
				userAccess = userAccess || ace.Mask != 0
			} else if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				return windowsPermissionError()
			}
		default:
			return windowsPermissionError()
		}
	}
	if !userAccess {
		return windowsPermissionError()
	}
	return nil
}

func windowsPermissionError() error {
	return fault.New("CONFIG-002", "Configuration storage must be owned by your Windows account and restricted to you, SYSTEM, and Administrators.", "Choose a private directory with --config-dir, or remove inherited access for other users in its Security properties.", 1)
}
