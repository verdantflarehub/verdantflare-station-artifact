//go:build !windows

package storage

import "os"

func syncObjects(root *os.Root) error {
	f, err := root.Open("objects")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
