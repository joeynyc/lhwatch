//go:build !unix

package collector

import "errors"

func controlBase() string { return "" }

func privateControlDir(string) (string, error) {
	return "", errors.New("SSH connection reuse requires Unix sockets")
}
