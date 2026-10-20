//go:build !unix

package collector

import "errors"

func browse(string) error { return errors.New("opening a browser is unsupported on this platform") }
