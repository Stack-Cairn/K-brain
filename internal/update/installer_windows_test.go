package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsInstallerCommand(t *testing.T) {
	cmd := InstallerCommand()
	if !strings.HasSuffix(strings.ToLower(cmd.Path), "powershell.exe") {
		t.Fatalf("%v", cmd)
	}
	script := cmd.Args[len(cmd.Args)-1]
	if !strings.Contains(script, "https://raw.githubusercontent.com/Stack-Cairn/K-brain/main/install.ps1") || !strings.Contains(script, "-InstallDir '") {
		t.Fatal(script)
	}
}

func TestWindowsInstallerPathPriority(t *testing.T) {
	installer, err := filepath.Abs("../../install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	const script = `
$ErrorActionPreference = 'Stop'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($env:TEST_INSTALLER, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw ($parseErrors | Out-String) }
$function = $ast.Find({ param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-PrioritizedPath'
}, $true)
if (-not $function) { throw 'Missing path priority function' }
. ([scriptblock]::Create($function.Extent.Text))
$dir = 'C:\Users\Test\AppData\Local\Programs\k-brain'
$old = 'C:\Users\Test\go\bin'
$env:TEST_INSTALL_HOME = $dir
$cases = @(
    @{ InputPath = ''; Expected = $dir },
    @{ InputPath = $old; Expected = "$dir;$old" },
    @{ InputPath = "$old;$dir"; Expected = "$dir;$old" },
    @{ InputPath = "$old;$($dir.ToUpper())\;$dir"; Expected = "$dir;$old" },
    @{ InputPath = ('"' + $dir + '";' + $old); Expected = "$dir;$old" },
    @{ InputPath = "%TEST_INSTALL_HOME%;$old;;"; Expected = "$dir;$old" },
    @{ InputPath = "$dir;$old"; Expected = "$dir;$old" },
    @{ InputPath = "$old;$dir-extra"; Expected = "$dir;$old;$dir-extra" }
)
foreach ($case in $cases) {
    $actual = Get-PrioritizedPath $case.InputPath $dir
    if ($actual -cne $case.Expected) { throw "Path mismatch: [$actual] != [$($case.Expected)]" }
    if ((Get-PrioritizedPath $actual $dir) -cne $actual) { throw 'Path update was not idempotent' }
}
$oldDir = Join-Path $env:TEST_INSTALL_ROOT 'old'
$newDir = Join-Path $env:TEST_INSTALL_ROOT 'new'
New-Item -ItemType Directory -Path $oldDir,$newDir | Out-Null
Set-Content -LiteralPath (Join-Path $oldDir 'kn.cmd') -Value '@echo k-brain dev'
Set-Content -LiteralPath (Join-Path $newDir 'kn.cmd') -Value '@echo k-brain v0.103.0'
$env:Path = "$oldDir;$newDir"
if ((Get-Command kn).Source -ne (Join-Path $oldDir 'kn.cmd')) { throw 'Fixture did not reproduce the old PATH priority' }
$env:Path = Get-PrioritizedPath $env:Path $newDir
if ((Get-Command kn).Source -ne (Join-Path $newDir 'kn.cmd')) { throw 'kn still resolves to the old version' }
Write-Output 'PATH priority verified'
`
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "test.ps1")
			if err := os.WriteFile(path, []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, shellPath, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
			cmd.Env = append(os.Environ(), "TEST_INSTALLER="+installer, "TEST_INSTALL_ROOT="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installer PATH test: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "PATH priority verified") {
				t.Fatalf("missing verification result: %s", out)
			}
		})
	}
}
