package names

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/birdtable/go-files/trust"
)

// maxFile is the most bytes the list or the exceptions file may hold.
const maxFile = 1 << 20

// rootOwner is the owner of a file the system owns, in the form of me, the
// running account: root on Unix, the local system account on Windows.
func rootOwner(me string) string {
	if strings.HasPrefix(me, "uid:") {
		return "uid:0"
	}
	return "S-1-5-18"
}

// reasons is why trust.ReadOwned refuses a file, by the sentinel it matches,
// in the order they are tried. Each says what is wrong and never names the
// file: the path of a list is the caller's, and an error's own text holds it.
var reasons = []struct {
	is  error
	why string
}{
	{trust.ErrNotPlain, "is not a regular file, or is reached through a link"},
	{trust.ErrMount, "lies on a mount whose owners are not judged"},
	{trust.ErrNotOwners, "is not owned by the running account or by the system"},
	{trust.ErrOthersWrite, "can be written by its group or by others"},
	{trust.ErrTooLong, "is longer than 1 MiB"},
	{trust.ErrReplaced, "was replaced while it was read"},
	{fs.ErrNotExist, "does not exist"},
	{fs.ErrPermission, "cannot be opened by the running account"},
}

// account is who the running account is. It is a variable for one reason: a
// test sets it to another account, to see a file the running account owns
// refused as not the owner's, which needs no root. Nothing else sets it.
var account = trust.Account

// readOwned reads the file at path as trust.ReadOwned does, up to maxFile
// bytes, for flag, the name of the flag that gave it. The owner is the
// running account; a file the system owns passes as a second try, so a list
// an administrator put in place is read. mounts are the caller's. Its error
// is the flag and a reason taken from the sentinel the refusal matches, never
// the error's own text, which holds the path, so nothing of it prints.
func readOwned(flag, path string, mounts ...trust.Who) ([]byte, error) {
	me, err := account()
	if err != nil || me == "" {
		return nil, fmt.Errorf("%s: the running account cannot be told, so no owner of the file can be judged", flag)
	}
	data, err := trust.ReadOwned(path, me, false, maxFile, mounts...)
	if err != nil && me != rootOwner(me) && errors.Is(err, trust.ErrNotOwners) && !errors.Is(err, trust.ErrMount) {
		var again error
		if data, again = trust.ReadOwned(path, rootOwner(me), false, maxFile, mounts...); again == nil {
			return data, nil
		}
		// The first refusal stands: the file is not the system's either.
	}
	if err == nil {
		return data, nil
	}
	for _, r := range reasons {
		if errors.Is(err, r.is) {
			return nil, fmt.Errorf("%s: the file %s", flag, r.why)
		}
	}
	return nil, fmt.Errorf("%s: the file cannot be read safely", flag)
}
