// Command zkscan คือโปรแกรมหลัก: ประกอบทุกส่วนเข้าด้วยกันและให้คำสั่ง CLI
package main

import (
	"os"
)

// version ถูกตั้งตอน build ด้วย -ldflags "-X main.version=..."
var version = "dev"

func main() {
	restrictUmask()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
