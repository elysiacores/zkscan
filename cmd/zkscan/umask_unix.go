//go:build unix

package main

import "syscall"

// restrictUmask ตั้ง umask 0027 (ไฟล์ใหม่ 0640, โฟลเดอร์ใหม่ 0750) และคืนค่า umask เดิม
func restrictUmask() int { return syscall.Umask(0o027) }
