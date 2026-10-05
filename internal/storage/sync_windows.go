package storage

import "os"

// Windows local development verifies file flushing, no-replace publication and
// process restart. Directory fsync is unavailable through os.File on Windows;
// production power-loss durability must be verified on the Linux storage volume.
func syncObjects(_ *os.Root) error { return nil }
