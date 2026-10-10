// Package licensing retains notices in distributed connector binaries.
package licensing

import _ "embed"

// Notices includes the project license and linked component notices.
//
//go:embed NOTICES.md
var Notices string
