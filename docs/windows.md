# Running GoOmni natively on Windows

GoOmni builds and runs as a native Windows program and can run as a Windows
service, without Docker, WSL or a separate Linux server. Protocol, storage and
HTTP behavior are the same as on Linux.

## Platform layer

The few operating-system calls GoOmni needs go through `src/pkg/platform`. Its
Unix implementation keeps the original calls; the Windows implementation
provides equivalents:

| Need | Unix | Windows |
|---|---|---|
| One process per database / media root | `flock` | `LockFileEx` (released when the handle closes or the process exits) |
| Free disk space metrics | `statfs` | `GetDiskFreeSpaceEx` |
| Media scan never follows links | `openat` + `O_NOFOLLOW` | `Lstat` check; symlinks and other reparse points (junctions) are skipped |
| SQLite URI | `file:/path` | `file:///C:/path` |
| Directory flush after rename | `fsync` on the directory | no-op (not allowed on Windows; NTFS journals metadata) |
| Private secret files (`init`, backups) | mode `0600` | protected access list: owner, SYSTEM, Administrators |
| Master key must be private | no group/other permission bits | only its owner, the current user, SYSTEM, Administrators and service accounts (`NT SERVICE\*`) may have access |

`src/cmd/service_windows.go` lets `goomni.exe` run under the Service Control
Manager:
- It switches to the executable's folder, so `.env`, `master.key` and
  `storages\` resolve as when `goomni rest` runs from that folder.
- It writes the log to `logs\goomni-service.log`. At 10 MB the file is renamed to
  `goomni-service.log.1` and a new one is started.
- A panic or fatal runtime error, which has no console to go to, is written to
  `logs\goomni-crash.log`.
- A service stop or system shutdown takes the same graceful path as Ctrl+C.
  An error exit reports a failure, so the service's recovery actions apply.

## Tests

Run `go test -tags purego ./...` on Windows with `CGO_ENABLED=0`, or use
`build.ps1 -Test`. The current CI runs on Linux only.
- Tests that create symlinks skip when Windows refuses symlinks without
  Developer Mode or administrator rights.
- `pkg/platform` checks the same link protections with directory junctions,
  which need no privilege.

Not covered by automated tests:
- Running under the Service Control Manager (it needs an elevated install).
- Live messenger logins, as on every platform.

## Build

On any machine with Go 1.26.9; the server needs only the resulting executable:

```powershell
.\scripts\windows\build.ps1 -Go <path>\go.exe -Test
```

The script builds `bin\windows\goomni.exe` with the pure-Go SQLite driver (no C
compiler) and copies the install scripts next to it.
- The admin UI is embedded only if `make ui-build` ran before, which needs Node.
- Without the UI, install with `APP_UI_ENABLED=false` (the default of
  `install-service.ps1`) and log in from the command line.

## Install as a service

Run these from an elevated PowerShell, in the folder that contains `goomni.exe`:

```powershell
.\install-service.ps1 -InstallDir C:\GoOmni -BaleWebClient
cd C:\GoOmni
.\goomni.exe login --device support --provider bale
```

The installer sets up the following:
- **Account:** the service runs as its own virtual account `NT SERVICE\GoOmni`,
  never as an administrator or LocalSystem.
- **Rights:** the account can read the program, `.env` and `master.key`, and can
  write only to `storages\` and `logs\`.
- **Restart:** the service restarts after a failure (1, 1, then 5 minutes).
- **Network:** GoOmni listens on `127.0.0.1` only, and no firewall rule is added.

The installer has these options:
- `-WithUI`: keep the embedded admin UI enabled.
- `-Port`: set the listening port of a new installation.

Keep `master.key` and `storages\` private: together they hold the logged-in
sessions. Back up `master.key` separately from the database.

## Upgrade and removal

To upgrade:
1. Build the new version.
2. Run `install-service.ps1` again. It stops the service, replaces the exe and
   starts it again.
3. `.env`, `master.key` and the data are kept. Follow the release's
   [upgrade notes](upgrade-goomni.md) as on any platform.

To remove the service, run `uninstall-service.ps1`. The install folder is kept
unless you pass `-DeleteData`.
