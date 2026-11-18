# Native PowerShell oracle. The pinned pwsh runs a case's fence SOURCE (the
# `function Example { & { <example> } *>&1 | Out-String -Stream -Width 120 }`
# text the case carries) the way any PowerShell host runs a function defined
# from text: the source is dot-sourced as a script block (no file, so error
# records show the same positions a console shows for a function body) and
# the function is invoked. Every string it emits is one stdout line (the
# example's console rendering at width 120, all streams merged in order); a
# terminating error ends the run with its message on stderr and status 1.
# Nothing here is taken from the fence: it is pwsh running the example.
#
#   bashy pwsh -NoLogo -NoProfile -NonInteractive -File oracle.ps1 -Path SOURCE.ps1 [-Function Example]
param([string]$Path, [string]$Function = 'Example')
$PSStyle.OutputRendering = 'PlainText'
$ErrorActionPreference = 'Continue'
$src = [System.IO.File]::ReadAllText($Path)
try {
    . ([scriptblock]::Create($src))
} catch {
    # the pinned pwsh cannot parse or define the example: not a run result
    $m = if ($_.Exception.InnerException) { $_.Exception.InnerException.Message } else { $_.Exception.Message }
    [Console]::Error.WriteLine("PARSE: " + ($m -replace "`r?`n", " "))
    exit 250
}
$cmd = Get-Command -Name $Function -CommandType Function -ErrorAction SilentlyContinue
if ($null -eq $cmd) { [Console]::Error.WriteLine("oracle: no function $Function in $Path"); exit 127 }
$utf8 = New-Object System.Text.UTF8Encoding($false)
$stdout = [Console]::OpenStandardOutput()
$status = 0
try {
    & $cmd | ForEach-Object {
        $line = ([string]$_) -replace "`r`n", "`n"
        $bytes = $utf8.GetBytes($line + "`n")
        $stdout.Write($bytes, 0, $bytes.Length)
    }
} catch {
    $stdout.Flush()
    $ex = $_.Exception
    $msg = if ($ex -and $ex.Message) { $ex.Message } else { [string]$_ }
    $stderr = [Console]::OpenStandardError()
    $b = $utf8.GetBytes((($msg -replace "`r`n", "`n")) + "`n")
    $stderr.Write($b, 0, $b.Length)
    $stderr.Flush()
    $status = 1
}
$stdout.Flush()
exit $status
