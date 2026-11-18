package control

import "golang.org/x/sys/windows"

// refusedErrno: der Fehler, mit dem ein Verbindungsversuch an einen Socket
// scheitert, an dem niemand lauscht (Winsock: WSAECONNREFUSED).
var refusedErrno error = windows.WSAECONNREFUSED
