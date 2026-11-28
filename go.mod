module github.com/bashsharp/bashsharp

go 1.27.0

toolchain go1.27.4

require (
	github.com/benhoyt/goawk v1.31.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-ole/go-ole v1.3.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sabhiram/go-gitignore v0.0.0-20210923224102-525f6e181f06 // indirect
	github.com/shirou/gopsutil/v4 v4.26.6 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/tklauser/ps v0.0.5-0.20260804061010-39c4acb07b31 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)

replace github.com/ergochat/readline => github.com/qiangli/readline v0.1.4-0.20261121101804-6ed12265d8dd

// Public dependency replacements are not inherited from imported modules.
replace github.com/benhoyt/goawk => github.com/qiangli/coreutils/third_party/goawk v0.0.0-20261126163007-96e079612fa5

// The same code as yoke's runewidth fork under bashsharp's own module path
// (external/runewidth/README.yoke.md), as cmd/bsh pins it.
replace github.com/mattn/go-runewidth => github.com/bashsharp/bashsharp/external/runewidth v0.0.0-20261122160503-f06822e26775

require (
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/creack/pty/v2 v2.0.1
	github.com/ergochat/readline v0.1.3
	github.com/qiangli/bash v0.0.0-20261122134408-5c52281f7136
	github.com/qiangli/coreutils v0.0.0-20261126163007-96e079612fa5
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
)
