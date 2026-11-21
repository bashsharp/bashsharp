// bsh is its own module so the certified shell's dependency graph is exactly
// Bash# + the Bash 5.3 engine + coreutils: no Yoke, Bashy or ycode module can
// enter it (TestBshImportBoundary). Build and test it with GOWORK=off.
module github.com/bashsharp/bashsharp/cmd/bsh

go 1.27.0

toolchain go1.27.4

require (
	github.com/bashsharp/bashsharp v0.0.0-20261121101805-58fa3c439d06
	github.com/qiangli/bash v0.0.0-20261121101805-42efe30317c4
	github.com/qiangli/coreutils v0.0.0-20261121101804-c5832f99ccf6
)

require (
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/benhoyt/goawk v1.31.0 // indirect
	github.com/blevesearch/go-faiss v1.1.5 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/ergochat/readline v0.1.3 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/google/nftables v0.3.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/mdlayher/netlink v1.11.2 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sabhiram/go-gitignore v0.0.0-20210923224102-525f6e181f06 // indirect
	github.com/shirou/gopsutil/v4 v4.26.6 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/tklauser/ps v0.0.5-0.20260804061010-39c4acb07b31 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.47.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

// Fork replaces are not inherited from dependencies; pin the same frozen forks
// the shell and coreutils are certified with.
replace github.com/ergochat/readline => github.com/qiangli/readline v0.1.4-0.20261013004858-df59d4e258e4

replace github.com/benhoyt/goawk => github.com/qiangli/coreutils/third_party/goawk v0.0.0-20261121101804-c5832f99ccf6
