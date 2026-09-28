package tools

import (
	"errors"
	"syscall"
)

// nodeErrorMessages are libuv's messages for the errno values the file
// tools surface (uv_strerror).
var nodeErrorMessages = map[syscall.Errno][2]string{
	syscall.ENOENT:       {"ENOENT", "no such file or directory"},
	syscall.EACCES:       {"EACCES", "permission denied"},
	syscall.EPERM:        {"EPERM", "operation not permitted"},
	syscall.ENOTDIR:      {"ENOTDIR", "not a directory"},
	syscall.EISDIR:       {"EISDIR", "is a directory"},
	syscall.ELOOP:        {"ELOOP", "too many symbolic links encountered"},
	syscall.ENAMETOOLONG: {"ENAMETOOLONG", "name too long"},
}

// nodeErrorCode returns the Node error code (error.code) for a file system
// error, or "" when it has none.
func nodeErrorCode(err error) string {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		if m, ok := nodeErrorMessages[errno]; ok {
			return m[0]
		}
	}
	return ""
}

// nodeFSError formats a file system error for the model as
// "<path>: <message>" ("<message>" without a path), e.g. "a.go: no such file
// or directory". Errors without a known errno keep their Go text.
func nodeFSError(err error, _, path string) string {
	msg := err.Error()
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		if m, ok := nodeErrorMessages[errno]; ok {
			msg = m[1]
		}
	}
	if path == "" {
		return msg
	}
	return path + ": " + msg
}
