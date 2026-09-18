$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false)
function Fail([int]$Code, [string]$Message) {
    $e = New-Object System.Exception($Message)
    $e.Data['rpcCode'] = $Code
    throw $e
}
try {
    $req = [Console]::In.ReadToEnd() | ConvertFrom-Json
    $p = $req.params
    Add-Type -AssemblyName UIAutomationClient
    Add-Type -AssemblyName UIAutomationTypes
    Add-Type -AssemblyName System.Windows.Forms
    Add-Type -AssemblyName System.Drawing
    Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class DesktopNative {
 [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
 [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
 [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
 [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
 [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
 [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
 [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
 [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
 [DllImport("user32.dll")] public static extern bool CloseDesktop(IntPtr desktop);
 [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
}
"@
    [void][DesktopNative]::SetProcessDPIAware()
    function DesktopReady {
        if (-not [Environment]::UserInteractive) { return $false }
        $h = [DesktopNative]::OpenInputDesktop(0, $false, 0x0100)
        if ($h -eq [IntPtr]::Zero) { return $false }
        [void][DesktopNative]::CloseDesktop($h)
        return $true
    }
    function Screenshot($Window) {
        $rect = New-Object DesktopNative+RECT
        if (-not [DesktopNative]::GetWindowRect($Window.MainWindowHandle, [ref]$rect)) { Fail 3 'Cannot read window bounds' }
        $screen = [System.Windows.Forms.SystemInformation]::VirtualScreen
        $left = [Math]::Max($rect.Left, $screen.Left)
        $top = [Math]::Max($rect.Top, $screen.Top)
        $width = [Math]::Min($rect.Right, $screen.Right) - $left
        $height = [Math]::Min($rect.Bottom, $screen.Bottom) - $top
        if ($width -le 0 -or $height -le 0 -or [int64]$width * $height -gt 40000000) { Fail 3 'Window is minimized, off-screen or too large' }
        $bitmap = New-Object System.Drawing.Bitmap($width, $height)
        $graphics = $null
        $stream = New-Object System.IO.MemoryStream
        try {
            $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
            $graphics.CopyFromScreen($left, $top, 0, 0, $bitmap.Size)
            $bitmap.Save($stream, [System.Drawing.Imaging.ImageFormat]::Jpeg)
            $bytes = $stream.ToArray()
            return @{jpegBase64 = [Convert]::ToBase64String($bytes); bytes = $bytes.Length}
        } finally {
            if ($null -ne $graphics) { $graphics.Dispose() }
            $bitmap.Dispose()
            $stream.Dispose()
        }
    }
    function Tree($Window) {
        $root = [System.Windows.Automation.AutomationElement]::FromHandle($Window.MainWindowHandle)
        if ($null -eq $root) { Fail 2 'Cannot read UI Automation tree' }
        $walker = [System.Windows.Automation.TreeWalker]::ControlViewWalker
        $nodes = New-Object 'System.Collections.Generic.List[System.Windows.Automation.AutomationElement]'
        $rows = New-Object 'System.Collections.Generic.List[object]'
        $stack = New-Object 'System.Collections.Generic.Stack[System.Windows.Automation.AutomationElement]'
        $stack.Push($root)
        while ($stack.Count -gt 0 -and $nodes.Count -lt 1500) {
            $node = $stack.Pop()
            $cur = $node.Current
            $actions = @()
            $value = ''
            $vp = $null
            if ($node.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$vp)) {
                $value = $vp.Current.Value
                if (-not $vp.Current.IsReadOnly) { $actions += 'set' }
            }
            foreach ($name in @('Invoke','SelectionItem','ExpandCollapse','Scroll','Text')) {
                $pt = ('System.Windows.Automation.' + $name + 'Pattern') -as [type]
                $pattern = $null
                if ($node.TryGetCurrentPattern($pt::Pattern, [ref]$pattern)) { $actions += $name }
            }
            $bounds = $cur.BoundingRectangle
            $position = @()
            $size = @()
            if (-not $bounds.IsEmpty) { $position = @($bounds.X, $bounds.Y); $size = @($bounds.Width, $bounds.Height) }
            $rows.Add([ordered]@{index=$nodes.Count; role=$cur.ControlType.ProgrammaticName; title=$cur.Name; value=$value; desc=$cur.AutomationId; position=$position; size=$size; actions=$actions; enabled=$cur.IsEnabled; focused=$cur.HasKeyboardFocus; runtimeId=($node.GetRuntimeId() -join '.')})
            $nodes.Add($node)
            $children = New-Object 'System.Collections.Generic.List[System.Windows.Automation.AutomationElement]'
            $child = $walker.GetFirstChild($node)
            while ($null -ne $child -and $children.Count -lt 1500) { $children.Add($child); $child = $walker.GetNextSibling($child) }
            for ($i = $children.Count - 1; $i -ge 0; $i--) { $stack.Push($children[$i]) }
        }
        $serialized = [ordered]@{pid=$Window.Id; handle=$Window.MainWindowHandle.ToInt64(); elements=@($rows.ToArray())} | ConvertTo-Json -Depth 10 -Compress
        $hash = [System.Security.Cryptography.SHA256]::Create()
        try { $revision = [Convert]::ToBase64String($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($serialized))) } finally { $hash.Dispose() }
        return @{nodes=$nodes; rows=@($rows.ToArray()); revision=$revision}
    }
    function RequiredText([string]$Name) {
        if ($null -eq $p.PSObject.Properties[$Name] -or $p.$Name -isnot [string]) { Fail -32602 "$Name must be a string" }
        return [string]$p.$Name
    }
    function Integer([string]$Name) {
        if ($null -eq $p.PSObject.Properties[$Name] -or ($p.$Name -isnot [int] -and $p.$Name -isnot [long])) { Fail -32602 "$Name must be an integer" }
        if ($p.$Name -lt [int]::MinValue -or $p.$Name -gt [int]::MaxValue) { Fail -32602 "$Name is out of range" }
        return [int]$p.$Name
    }
    function LiteralKeys([string]$Text) {
        $b = New-Object Text.StringBuilder
        foreach ($c in $Text.ToCharArray()) {
            switch ([string]$c) {
                '{' { [void]$b.Append('{{}') }
                '}' { [void]$b.Append('{}}') }
                "`r" { }
                "`n" { [void]$b.Append('{ENTER}') }
                "`t" { [void]$b.Append('{TAB}') }
                default {
                    if ('+^%~()[]'.Contains([string]$c)) { [void]$b.Append('{' + $c + '}') } else { [void]$b.Append($c) }
                }
            }
        }
        return $b.ToString()
    }
    function KeyCombo([string]$Key) {
        $parts = $Key.ToLowerInvariant().Split('+')
        $prefix = ''
        for ($i=0; $i -lt $parts.Count-1; $i++) {
            switch ($parts[$i]) {
                'ctrl' { $prefix += '^' }
                'control' { $prefix += '^' }
                'alt' { $prefix += '%' }
                'shift' { $prefix += '+' }
                default { Fail 6 ('Unsupported key modifier: ' + $parts[$i]) }
            }
        }
        $last = $parts[-1]
        $names = @{return='ENTER'; enter='ENTER'; escape='ESC'; esc='ESC'; tab='TAB'; backspace='BACKSPACE'; delete='DELETE'; home='HOME'; end='END'; left='LEFT'; right='RIGHT'; up='UP'; down='DOWN'; prior='PGUP'; next='PGDN'; pageup='PGUP'; pagedown='PGDN'; space=' '}
        if ($names.ContainsKey($last)) {
            if ($last -eq 'space') { return $prefix + ' ' }
            return $prefix + '{' + $names[$last] + '}'
        }
        if ($last -match '^f([1-9]|1[0-6])$') { return $prefix + '{' + $last.ToUpperInvariant() + '}' }
        if ($last -match '^[a-z0-9]$') { return $prefix + $last }
        Fail 6 ('Unsupported key: ' + $Key)
    }
    $result = $null
    if ($req.method -in @('permissions.status', 'permissions.request')) {
        $ready = DesktopReady
        $result = @{accessibility=$ready; screenRecording=$false; pending=$false; hint='Windows interactive desktop readiness only; per-app UIA and screenshot access is checked when called. Elevated/protected apps may reject automation.'}
        if ($ready) {
            try {
                $root = [System.Windows.Automation.AutomationElement]::RootElement
                $result.accessibility = ($null -ne $root)
                $bitmap = New-Object System.Drawing.Bitmap(1,1)
                $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
                try { $graphics.CopyFromScreen(0,0,0,0,$bitmap.Size); $result.screenRecording=$true } finally { $graphics.Dispose(); $bitmap.Dispose() }
            } catch { $result.hint = $_.Exception.Message }
        }
    } elseif ($req.method -eq 'apps') {
        $foreground = [DesktopNative]::GetForegroundWindow()
        $result = @(Get-Process | Where-Object {$_.MainWindowHandle -ne 0} | ForEach-Object {
            @{name=$_.MainWindowTitle; bundleId=$_.ProcessName; pid=$_.Id; active=($_.MainWindowHandle -eq $foreground)}
        })
    } else {
        if (-not (DesktopReady)) { Fail 7 'No accessible interactive desktop (locked or non-interactive session)' }
        $app = RequiredText 'app'
        if ([string]::IsNullOrWhiteSpace($app)) { Fail -32602 'app is required' }
        $matches = @(Get-Process | Where-Object {$_.MainWindowHandle -ne 0 -and ($_.ProcessName -eq $app -or $_.MainWindowTitle -eq $app -or [string]$_.Id -eq $app)})
        if ($matches.Count -eq 0) { Fail 1 'App not found; use apps() to select a running window' }
        if ($matches.Count -ne 1) { Fail 1 'App is ambiguous; use its PID from apps()' }
        $window = $matches[0]
        if ($req.method -eq 'screenshot') {
            if ([DesktopNative]::GetForegroundWindow() -ne $window.MainWindowHandle) { Fail 3 'Window is not foreground; screenshot would include other windows' }
            $result = Screenshot $window
        } else {
            $tree = Tree $window
            if ($req.method -in @('state','ax')) {
                $result = @{app=$app; elements=$tree.rows; revision=$tree.revision}
                if ($req.method -eq 'state') {
                    try {
                        if ([DesktopNative]::GetForegroundWindow() -ne $window.MainWindowHandle) { Fail 3 'Window is not foreground' }
                        $result.screenshot = Screenshot $window
                    } catch { $result.screenshot = @{error=$_.Exception.Message} }
                }
            } else {
                if ($p.expectedRevision -ne $tree.revision) { Fail 4 'state changed - re-read' }
                $element = $null
                if ($null -ne $p.PSObject.Properties['index']) {
                    $index = Integer 'index'
                    if ($index -lt 0 -or $index -ge $tree.nodes.Count) { Fail 5 'Element index out of range' }
                    $element = $tree.nodes[$index]
                    if (-not $element.Current.IsEnabled) { Fail 6 'Element is disabled' }
                }
                if ($req.method -in @('set','select','menu','scroll') -and $null -eq $element) { Fail -32602 'index is required' }
                $keys = $null
                if ($req.method -eq 'type') { $keys = LiteralKeys (RequiredText 'text') }
                if ($req.method -eq 'press') { $keys = KeyCombo (RequiredText 'key') }
                if ([DesktopNative]::GetForegroundWindow() -ne $window.MainWindowHandle) {
                    [void][DesktopNative]::ShowWindow($window.MainWindowHandle, 9)
                    [void][DesktopNative]::SetForegroundWindow($window.MainWindowHandle)
                    Start-Sleep -Milliseconds 120
                }
                if ([DesktopNative]::GetForegroundWindow() -ne $window.MainWindowHandle) { Fail 6 'Cannot focus requested app; action was not sent' }
                switch ($req.method) {
                    'click' {
                        if ($null -ne $element) {
                            $pattern = $null
                            if ($element.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern,[ref]$pattern)) { $pattern.Invoke() }
                            elseif ($element.TryGetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern,[ref]$pattern)) { $pattern.Select() }
                            else { Fail 6 'Element has no Invoke or SelectionItem pattern; use coordinates explicitly' }
                        } else {
                            $x = Integer 'x'
                            $y = Integer 'y'
                            $rect = $tree.nodes[0].Current.BoundingRectangle
                            if ($x -lt $rect.Left -or $x -ge $rect.Right -or $y -lt $rect.Top -or $y -ge $rect.Bottom) { Fail -32602 'Coordinates are outside requested window (absolute screen pixels required)' }
                            $point = New-Object System.Windows.Point($x,$y)
                            $hit = [System.Windows.Automation.AutomationElement]::FromPoint($point)
                            if ($null -eq $hit -or $hit.Current.ProcessId -ne $window.Id) { Fail 6 'Requested point is obscured by another app' }
                            if (-not [DesktopNative]::SetCursorPos($x,$y)) { Fail 6 'Cannot move pointer' }
                            [DesktopNative]::mouse_event(2,0,0,0,[UIntPtr]::Zero)
                            [DesktopNative]::mouse_event(4,0,0,0,[UIntPtr]::Zero)
                        }
                    }
                    {$_ -in @('type','press')} { [System.Windows.Forms.SendKeys]::SendWait($keys) }
                    'set' {
                        $pattern = $null
                        if (-not $element.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern,[ref]$pattern)) { Fail 6 'Element does not support ValuePattern' }
                        if ($pattern.Current.IsReadOnly) { Fail 6 'Element value is read-only' }
                        $pattern.SetValue((RequiredText 'value'))
                    }
                    'select' {
                        $pattern = $null
                        if (-not $element.TryGetCurrentPattern([System.Windows.Automation.TextPattern]::Pattern,[ref]$pattern)) { Fail 6 'Element does not support TextPattern' }
                        $target = RequiredText 'target'
                        if ($target.Length -eq 0) { Fail -32602 'target cannot be empty' }
                        $range = $pattern.DocumentRange.FindText($target,$false,$false)
                        if ($null -eq $range) { Fail 6 'Text not found in element' }
                        $range.Select()
                    }
                    'menu' {
                        $action = RequiredText 'action'
                        $pattern = $null
                        if (-not $element.TryGetCurrentPattern([System.Windows.Automation.ExpandCollapsePattern]::Pattern,[ref]$pattern)) { Fail 6 'Element does not support ExpandCollapsePattern' }
                        switch ($action.ToLowerInvariant()) {
                            'expand' { $pattern.Expand() }
                            'collapse' { $pattern.Collapse() }
                            default { Fail 6 'Supported menu actions: expand, collapse' }
                        }
                    }
                    'scroll' {
                        $dir = RequiredText 'dir'
                        $n = 1
                        if ($null -ne $p.PSObject.Properties['clicks']) { $n = Integer 'clicks' }
                        if ($n -lt 1 -or $n -gt 100) { Fail -32602 'clicks must be 1..100' }
                        $pattern = $null
                        if (-not $element.TryGetCurrentPattern([System.Windows.Automation.ScrollPattern]::Pattern,[ref]$pattern)) { Fail 6 'Element does not support ScrollPattern' }
                        $h = [System.Windows.Automation.ScrollAmount]::NoAmount
                        $v = [System.Windows.Automation.ScrollAmount]::NoAmount
                        switch ($dir) {
                            'up' { $v = [System.Windows.Automation.ScrollAmount]::SmallDecrement }
                            'down' { $v = [System.Windows.Automation.ScrollAmount]::SmallIncrement }
                            'left' { $h = [System.Windows.Automation.ScrollAmount]::SmallDecrement }
                            'right' { $h = [System.Windows.Automation.ScrollAmount]::SmallIncrement }
                            default { Fail -32602 'Unknown scroll direction' }
                        }
                        for ($i=0; $i -lt $n; $i++) { $pattern.Scroll($h,$v) }
                    }
                    default { Fail -32601 'Unknown method' }
                }
                $result = @{ok=$true}
            }
        }
    }
    @{result=$result} | ConvertTo-Json -Depth 14 -Compress
} catch {
    $code = 6
    if ($_.Exception.Data.Contains('rpcCode')) { $code = [int]$_.Exception.Data['rpcCode'] }
    @{error=@{code=$code; message=$_.Exception.Message}} | ConvertTo-Json -Depth 5 -Compress
}
