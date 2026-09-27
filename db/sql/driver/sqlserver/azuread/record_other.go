//go:build !unix

package azuread

import "os"

// writableByOthers is empty outside Unix, where Go reports no owner. On Windows, who may write a
// file is in its ACL, which the record inherits from the user's profile, and not in the mode bits
// Go reports.
func writableByOthers(os.FileInfo) string { return "" }
