package main

import (
	"golang.org/x/crypto/ssh"

	"github.com/codewiresh/codewire/internal/envshell"
)

func codewireHostKeyCallback() (ssh.HostKeyCallback, error) {
	return envshell.HostKeyCallback(defaultKnownHostsPath())
}
