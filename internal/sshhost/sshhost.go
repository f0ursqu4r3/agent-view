// Package sshhost validates the ssh destination of a remote host.
package sshhost

import "regexp"

// hostRE accepts host names and user@host: no leading "-" (ssh would read an
// option), no spaces, no shell characters.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@:-]*$`)

// Valid reports whether s is a host name or user@host that is safe to pass
// to ssh.
func Valid(s string) bool { return hostRE.MatchString(s) }
