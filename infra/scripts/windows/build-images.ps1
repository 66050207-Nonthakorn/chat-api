$ErrorActionPreference = "Stop"

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\..\.."))
. (Join-Path $PSScriptRoot "source-revision.ps1")
$revision = Get-CleanSourceRevision $repositoryRoot "Build a clean, committed source tree so the image revision identifies its source."

Push-Location $repositoryRoot
try {
  foreach ($service in @("api", "worker")) {
    $tag = "cloudchat-${service}:${revision}"
    $imageIdPath = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudchat-${service}-${revision}.iid")
    & docker build --build-arg "VCS_REF=$revision" --target $service --iidfile $imageIdPath --tag $tag .
    if ($LASTEXITCODE -ne 0) {
      throw "Docker build failed for $service."
    }
    $imageId = (Get-Content -LiteralPath $imageIdPath -Raw).Trim()
    Remove-Item -LiteralPath $imageIdPath -Force
    $imageRevision = (& docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' $tag).Trim()
    if ($LASTEXITCODE -ne 0 -or $imageRevision -ne $revision -or $imageId -notmatch "^sha256:[0-9a-f]{64}$") {
      throw "Built $service image did not retain the requested source revision and content ID."
    }
    Write-Output "$service local tag: $tag"
    Write-Output "$service local image ID: $imageId"
  }
}
finally {
  Pop-Location
}
