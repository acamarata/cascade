$ErrorActionPreference = 'Stop'

$packageName = 'cascade'
$softwareName = 'Cascade*'

$uninstalled = $false
[array]$key = Get-UninstallRegistryKey -SoftwareName $softwareName

if ($key.Count -eq 1) {
  $key | ForEach-Object {
    $file = "$($_.UninstallString)"

    if ($_.UninstallString -match 'msiexec') {
      $silentArgs = "/x $($_.PSChildName) /qn /norestart"
      $file = 'msiexec'
    }

    Uninstall-ChocolateyPackage `
      -PackageName $packageName `
      -FileType 'MSI' `
      -SilentArgs "$($_.PSChildName) /qn /norestart" `
      -ValidExitCodes @(0, 3010, 1605, 1614, 1641) `
      -File $file
  }
} elseif ($key.Count -eq 0) {
  Write-Warning "$packageName has already been uninstalled by other means."
} elseif ($key.Count -gt 1) {
  Write-Warning "$($key.Count) matches found. Uninstall the program manually."
  Write-Warning "($($key | ForEach-Object { $_.DisplayName }) | Out-String)"
}
