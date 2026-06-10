$ErrorActionPreference = 'Stop'

$packageArgs = @{
  packageName    = 'cascade'
  fileType       = 'MSI'
  url64bit       = 'https://github.com/acamarata/cascade/releases/download/v0.1.0/cascade_0.1.0_windows_x64.msi'
  checksum64     = 'PLACEHOLDER_UPDATED_BY_CI'
  checksumType64 = 'sha256'
  silentArgs     = '/qn /norestart'
  validExitCodes = @(0, 3010, 1641)
}

Install-ChocolateyPackage @packageArgs
