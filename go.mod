module github.com/bashsharp/bashsharp

go 1.27.0

toolchain go1.27.2

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
)

replace github.com/ergochat/readline => github.com/qiangli/readline v0.1.4-0.20261013004858-df59d4e258e4

require (
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/creack/pty v1.1.24
	github.com/creack/pty/v2 v2.0.1
	github.com/ergochat/readline v0.1.3
	github.com/go-quicktest/qt v1.101.0
	github.com/google/go-cmp v0.7.0
	github.com/qiangli/bash v0.0.0-20261118050932-05671b769b06
	github.com/qiangli/coreutils v0.0.0-20261118041055-ababb9f1c79a
	github.com/qiangli/yoke v0.0.0-20261118041247-79fe8f90bb05
	golang.org/x/mod v0.41.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
	golang.org/x/text v0.42.0
	golang.org/x/tools v0.50.0
)

require (
	github.com/qiangli/yoke/pkg/oci v0.0.0-20261115042953-d160182c8479
	github.com/qiangli/yoke/pkg/llmgw v0.0.0-20261115042953-d160182c8479
	github.com/qiangli/yoke/pkg/provider v0.0.0-20261115042953-d160182c8479
	github.com/qiangli/yoke/external/otel v0.0.0-20261115042953-d160182c8479
)

// Public dependency replacements are not inherited from imported modules.
replace github.com/benhoyt/goawk => github.com/qiangli/coreutils/third_party/goawk v0.0.0-20261112194059-6dd7a3ee1858
replace github.com/jaegertracing/jaeger => github.com/qiangli/jaeger v0.0.0-20260426223533-5aaa7eb1f040
replace github.com/perses/perses => github.com/qiangli/perses v0.0.0-20260426190059-de437951b5e6
replace github.com/ollama/ollama => github.com/qiangli/ollama v0.0.0-20261016181410-b0d0bbba93ab
replace go.podman.io/podman/v6 => github.com/qiangli/podman/v6 v6.0.0-20261013004941-fa029d9c82fb
replace github.com/gtsteffaniak/filebrowser/backend => github.com/qiangli/filebrowser-quantum/backend v0.0.0-20261115042727-40f3c84d55a9
replace github.com/odvcencio/gotreesitter => github.com/qiangli/gotreesitter v0.0.0-20261103194740-9471d547a17c
replace modernc.org/sqlite => github.com/qiangli/sqlite v1.50.2-0.20261018085128-5fb52bf328cb
replace github.com/go-git/go-billy/v5 => github.com/qiangli/yoke/external/go-billy v0.0.0-20261030181600-621d3b7f135f
replace github.com/hashicorp/go-multierror => github.com/qiangli/yoke/external/multierror v0.0.0-20261021020216-636e2fd289e9
replace github.com/hashicorp/errwrap => github.com/qiangli/yoke/external/errwrap v0.0.0-20261021020216-636e2fd289e9
replace github.com/cyphar/filepath-securejoin => github.com/qiangli/yoke/external/securejoin v0.0.0-20261021020216-636e2fd289e9
replace github.com/mattn/go-runewidth => github.com/qiangli/yoke/external/runewidth v0.0.0-20261025041311-99ef13338402
