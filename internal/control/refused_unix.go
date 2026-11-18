//go:build !windows

package control

import "syscall"

// refusedErrno: der Fehler, mit dem ein Verbindungsversuch an einen Socket
// scheitert, an dem niemand lauscht.
var refusedErrno error = syscall.ECONNREFUSED
