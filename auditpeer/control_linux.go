//go:build linux

package auditpeer

import (
	"encoding/binary"
	"syscall"

	"golang.org/x/sys/unix"
)

// Rights are never accepted. Room for a few lets us explicitly dispose installed
// descriptors; Linux closes excess rights it could not fit into this buffer.
const bufferedRights = 8

var controlBytes = syscall.CmsgSpace(syscall.SizeofUcred) + syscall.CmsgSpace(bufferedRights*4)

// This is private and only consumes recvmsg-produced ancillary bytes. Do not
// feed model/request bytes into a function that closes kernel-installed FDs.
func decodeControl(control []byte) (credentials Credentials, err error) {
	rights := map[int]bool{}
	defer func() {
		for fd := range rights {
			if syscall.Close(fd) != nil {
				credentials, err = Credentials{}, ErrControl
			}
		}
	}()
	count := 0
	invalid := false
	headerBytes := syscall.CmsgLen(0)
	sizeBytes := syscall.SizeofCmsghdr - 8
	for offset := 0; offset < len(control); {
		remaining := control[offset:]
		if len(remaining) < headerBytes {
			invalid = true
			break
		}
		var length uint64
		if sizeBytes == 8 {
			length = binary.NativeEndian.Uint64(remaining[:8])
		} else if sizeBytes == 4 {
			length = uint64(binary.NativeEndian.Uint32(remaining[:4]))
		} else {
			return Credentials{}, ErrControl
		}
		level := int32(binary.NativeEndian.Uint32(remaining[sizeBytes : sizeBytes+4]))
		kind := int32(binary.NativeEndian.Uint32(remaining[sizeBytes+4 : sizeBytes+8]))
		if length < uint64(headerBytes) {
			invalid = true
			break
		}
		end := length
		if end > uint64(len(remaining)) {
			end = uint64(len(remaining))
			invalid = true
		}
		body := remaining[headerBytes:int(end)]
		switch {
		case level == syscall.SOL_SOCKET && kind == syscall.SCM_RIGHTS:
			invalid = true
			for at := 0; at+4 <= len(body); at += 4 {
				fd := int(int32(binary.NativeEndian.Uint32(body[at : at+4])))
				if fd >= 0 {
					rights[fd] = true
				}
			}
		case level == syscall.SOL_SOCKET && kind == unix.SCM_PIDFD:
			invalid = true
			if len(body) >= 4 {
				fd := int(int32(binary.NativeEndian.Uint32(body[:4])))
				if fd >= 0 {
					rights[fd] = true
				}
			}
		case level == syscall.SOL_SOCKET && kind == syscall.SCM_CREDENTIALS:
			count++
			if len(body) != syscall.SizeofUcred {
				invalid = true
			} else {
				credentials = Credentials{PID: int32(binary.NativeEndian.Uint32(body[:4])),
					UID: binary.NativeEndian.Uint32(body[4:8]), GID: binary.NativeEndian.Uint32(body[8:12])}
				if credentials.PID <= 0 {
					invalid = true
				}
			}
		default:
			invalid = true
		}
		if length > uint64(len(remaining)) {
			break
		}
		aligned := syscall.CmsgSpace(int(length) - headerBytes)
		if aligned <= 0 {
			invalid = true
			break
		}
		if aligned > len(remaining) {
			// A final cmsg may omit alignment padding; its declared body must fit.
			if int(length) != len(remaining) {
				invalid = true
			}
			break
		}
		offset += aligned
	}
	if invalid || count != 1 {
		return Credentials{}, ErrControl
	}
	return credentials, nil
}
