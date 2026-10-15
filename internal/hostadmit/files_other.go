//go:build !linux

package hostadmit

import "errors"

var errLinuxOnly = errors.New("the local transport runs only on Linux")

func readFile(string) (string, bool, error) { return "", false, errLinuxOnly }

func writeFile(string, string, uint32) error { return errLinuxOnly }
