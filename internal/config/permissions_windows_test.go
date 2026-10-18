package config

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func readConcurrentSnapshot(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Snapshot readers must share deletion so they do not block atomic replacement.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return io.ReadAll(file)
}

func TestWindowsPrivateDirectoryAndFile(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "new-config"))
	must(t, store.Save(Credentials{AuthToken: "test-token"}))
	for _, path := range []string{store.Dir(), store.Path()} {
		info, err := os.Stat(path)
		must(t, err)
		must(t, checkPrivatePermissions(path, info))
	}
	credentials, err := store.Load()
	must(t, err)
	if credentials.AuthToken != "test-token" {
		t.Fatal("private ACL prevented loading saved credentials")
	}
}

func TestWindowsRejectsAccessForOtherUsers(t *testing.T) {
	for _, target := range []string{"directory", "credentials"} {
		t.Run(target, func(t *testing.T) {
			store := New(privateDir(t))
			must(t, store.Save(Credentials{AuthToken: "test-token"}))
			path := store.Path()
			if target == "directory" {
				path = store.Dir()
			}
			user, err := currentUserSID()
			must(t, err)
			setTestDACL(t, path, "O:"+user.String()+"D:P(A;OICI;FA;;;"+user.String()+")(A;OICI;FR;;;WD)")
			_, err = store.Load()
			assertFaultCode(t, err, "CONFIG-002")
			assertFaultCode(t, store.Save(Credentials{AuthToken: "replacement"}), "CONFIG-002")
			assertFaultCode(t, store.Logout(), "CONFIG-002")
		})
	}
}

func TestWindowsAllowsSystemAndAdministrators(t *testing.T) {
	store := New(privateDir(t))
	user, err := currentUserSID()
	must(t, err)
	setTestDACL(t, store.Dir(), "O:"+user.String()+"D:P(A;OICI;FA;;;"+user.String()+")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	must(t, store.Save(Credentials{AuthToken: "test-token"}))
	_, err = store.Load()
	must(t, err)
}

func TestWindowsRejectsNullDACL(t *testing.T) {
	store := New(privateDir(t))
	user, err := currentUserSID()
	must(t, err)
	setTestDACL(t, store.Dir(), "O:"+user.String()+"D:NO_ACCESS_CONTROL")
	_, err = store.Load()
	assertFaultCode(t, err, "CONFIG-002")
}

func setTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	must(t, err)
	owner, _, err := descriptor.Owner()
	must(t, err)
	dacl, _, err := descriptor.DACL()
	must(t, err)
	must(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil))
	runtime.KeepAlive(descriptor)
}
