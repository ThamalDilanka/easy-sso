//go:build windows

package store

import (
	"errors"
	"golang.org/x/sys/windows"
	"unsafe"
)

func private(path string, dir bool) error {
	return Protect(path, dir)
}
func Protect(path string, dir bool) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	flags := ""
	if dir {
		flags = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return errors.New("cannot establish current-user-only Windows ACL")
	}
	return ValidatePrivate(path)
}
func ValidatePrivate(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, u.User.Sid) {
		return errors.New("storage must be owned by the current Windows user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("storage ACL must disable inherited access")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return errors.New("storage ACL must grant only the current Windows user")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !windows.EqualSid(sid, u.User.Sid) {
		return errors.New("storage ACL grants access beyond the current Windows user")
	}
	return nil
}
func replace(from, to string) error {
	f, e := windows.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	t, e := windows.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func syncDir(string) error { return nil }
