//go:build unix

package azuread

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestARecordOthersCouldHaveWrittenIsIgnored: the record decides which account in the user's own
// cache signs in silently, so one planted in a directory others can write, such as an
// authentication_record_path in a shared one, would decide it for them. A record another user
// owns, or may write, is ignored with a warning, and the person is asked.
func TestARecordOthersCouldHaveWrittenIsIgnored(t *testing.T) {
	usePersistentCache(t)
	warnings := captureWarnings(t)
	path := filepath.Join(t.TempDir(), "record.json")
	writeRecordFile(t, path, standInRecord)
	req := persistent(sqlserver.AuthMethodInteractive, dsconfig.Auth{AuthenticationRecordPath: path})
	assert.Equal(t, standInRecord, readRecord(path, req), "the owner's own 0600 record is read")

	require.NoError(t, os.Chmod(path, 0o620))
	assert.Zero(t, readRecord(path, req))
	require.Len(t, *warnings, 1)
	assert.Equal(t, "azuread: the record of the last sign-in for "+standInTarget+" is ignored, so the person is "+
		"asked again: "+path+" can be written by other users; keep it in a directory only this user can write "+
		"(auth.authentication_record_path)", (*warnings)[0])

	cred := nowCredential()
	remembered := start(t, req, cred)
	assert.False(t, remembered.hasRecord())
	_, authenticates := cred.counts()
	assert.Equal(t, 1, authenticates, "so the person was asked")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "and the sign-in replaced it with a private one")
	assert.Equal(t, standInRecord, readRecord(path, req))

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	assert.Zero(t, readRecord(path, req), "a directory is no record")
}

// TestWhoCouldHaveWrittenARecord: a file of the user running the process, 0600 or read-only, is
// theirs alone; another user's, or one its group or everyone may write, is not. Another user's
// file cannot be made without root, so its owner is changed in what Stat reports.
func TestWhoCouldHaveWrittenARecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.json")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	for mode, want := range map[os.FileMode]string{
		0o600: "",
		0o400: "",
		0o640: "",
		0o620: "can be written by other users",
		0o602: "can be written by other users",
	} {
		require.NoError(t, os.Chmod(path, mode))
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equalf(t, want, writableByOthers(info), "mode %v", mode)
	}

	require.NoError(t, os.Chmod(path, 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, "belongs to another user", writableByOthers(ownedByAnother{info}))
}

// ownedByAnother is a file's info with another user as its owner.
type ownedByAnother struct{ os.FileInfo }

func (o ownedByAnother) Sys() any {
	st := *o.FileInfo.Sys().(*syscall.Stat_t)
	st.Uid++
	return &st
}
