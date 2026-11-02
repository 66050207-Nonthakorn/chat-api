function Get-CleanSourceRevision([string]$RepositoryRoot, [string]$DirtyMessage) {
  $revision = (& git -C $RepositoryRoot rev-parse --verify HEAD).Trim()
  if ($LASTEXITCODE -ne 0 -or $revision -notmatch "^[0-9a-f]{40}$") {
    throw "Could not resolve a full Git source revision."
  }
  $workingTree = & git -C $RepositoryRoot status --porcelain
  if ($LASTEXITCODE -ne 0 -or $workingTree) {
    throw $DirtyMessage
  }
  return $revision
}
