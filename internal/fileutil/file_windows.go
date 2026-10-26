package fileutil

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

// Open rejects reparse points and gives private files an owner-only DACL.
func Open(path string, flags int, mode os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flags&os.O_RDWR != 0 {
		access |= windows.GENERIC_WRITE
	} else if flags&os.O_WRONLY != 0 {
		access = windows.GENERIC_WRITE
	}
	var security *windows.SecurityAttributes
	var descriptor *windows.SECURITY_DESCRIPTOR
	if mode.Perm() == 0600 {
		token := windows.GetCurrentProcessToken()
		user, err := token.GetTokenUser()
		if err != nil {
			return nil, err
		}
		descriptor, err = windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
		if err != nil {
			return nil, err
		}
		security = &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
		access |= windows.WRITE_DAC
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		disposition = windows.OPEN_ALWAYS
		if flags&os.O_EXCL != 0 {
			disposition = windows.CREATE_NEW
		}
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, security, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return nil, errors.New("reparse points are not allowed")
	}
	if descriptor != nil {
		dacl, _, err := descriptor.DACL()
		if err == nil {
			err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		}
		if err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

// Lock takes a nonblocking exclusive byte-range lock until the file is closed.
func Lock(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
