//go:build windows

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceLogRotatesAtTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goomni-service.log")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("o"), logMaxBytes-10), 0600))
	r, err := openRotating(path)
	require.NoError(t, err)
	_, err = r.Write([]byte("fits\n"))
	require.NoError(t, err)
	_, err = r.Write([]byte("this line passes the cap\n"))
	require.NoError(t, err)
	require.NoError(t, r.Close())

	old, err := os.Stat(path + ".1")
	require.NoError(t, err)
	require.Equal(t, int64(logMaxBytes-10+5), old.Size(), "the full file is kept as .1")
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "this line passes the cap\n", string(current))

	// a second rotation replaces the older .1
	r, err = openRotating(path)
	require.NoError(t, err)
	_, err = r.Write(bytes.Repeat([]byte("n"), logMaxBytes))
	require.NoError(t, err)
	require.NoError(t, r.Close())
	old, err = os.Stat(path + ".1")
	require.NoError(t, err)
	require.Equal(t, int64(len("this line passes the cap\n")), old.Size())
}
