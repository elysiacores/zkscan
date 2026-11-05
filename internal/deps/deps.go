//go:build tools

// Package deps keeps build dependencies pinned in go.mod.
package deps

import (
	_ "github.com/gofiber/fiber/v2"
	_ "golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)
