//go:build !unix

package main

// restrictUmask ไม่ทำอะไรบนแพลตฟอร์มที่ไม่มี umask
func restrictUmask() int { return 0 }
