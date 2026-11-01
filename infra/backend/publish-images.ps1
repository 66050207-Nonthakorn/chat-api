param(
  [Parameter(Mandatory = $true)][string]$Region,
  [Parameter(Mandatory = $true)][string]$ApiRepository,
  [Parameter(Mandatory = $true)][string]$WorkerRepository
)

$ErrorActionPreference = "Stop"

$repositoryRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$revision = (& git -C $repositoryRoot rev-parse --verify HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $revision -notmatch "^[0-9a-f]{40}$") {
  throw "Could not resolve a full Git source revision."
}
$workingTree = & git -C $repositoryRoot status --porcelain
if ($LASTEXITCODE -ne 0 -or $workingTree) {
  throw "Publish only a clean commit whose CI checks have passed."
}

$apiRegistry = $ApiRepository.Split("/", 2)[0]
$workerRegistry = $WorkerRepository.Split("/", 2)[0]
if ($apiRegistry -ne $workerRegistry -or $apiRegistry -notmatch "\.dkr\.ecr\.") {
  throw "API and worker repositories must use the same Amazon ECR registry."
}

$password = & aws ecr get-login-password --region $Region
if ($LASTEXITCODE -ne 0) {
  throw "Could not authenticate to Amazon ECR using the current AWS identity."
}
$password | & docker login --username AWS --password-stdin $apiRegistry | Out-Null
$password = $null
if ($LASTEXITCODE -ne 0) {
  throw "Docker could not authenticate to the configured ECR registry."
}

function Get-RepositoryName([string]$RepositoryUrl) {
  return $RepositoryUrl.Substring($RepositoryUrl.IndexOf("/") + 1)
}

function Get-ExistingDigest([string]$RepositoryUrl) {
  $repositoryName = Get-RepositoryName $RepositoryUrl
  $digest = & aws ecr describe-images --region $Region --repository-name $repositoryName --image-ids "imageTag=$revision" --query "imageDetails[0].imageDigest" --output text 2>$null
  if ($LASTEXITCODE -eq 0 -and $digest -match "^sha256:[0-9a-f]{64}$") {
    return $digest.Trim()
  }
  return $null
}

function Publish-Image([string]$Service, [string]$RepositoryUrl) {
  $existing = Get-ExistingDigest $RepositoryUrl
  if ($existing) {
    Write-Output "$RepositoryUrl@$existing"
    return
  }

  $metadataPath = Join-Path ([System.IO.Path]::GetTempPath()) ("cloudchat-${Service}-${revision}-" + [guid]::NewGuid().ToString("N") + ".json")
  try {
    Push-Location $repositoryRoot
    try {
      & docker buildx build --platform linux/amd64 --push --build-arg "VCS_REF=$revision" --target $Service --tag "$RepositoryUrl`:$revision" --metadata-file $metadataPath .
      if ($LASTEXITCODE -ne 0) {
        throw "Image publish failed for $Service."
      }
    }
    finally {
      Pop-Location
    }

    $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
    $digest = $metadata.'containerimage.digest'
    if ($digest -notmatch "^sha256:[0-9a-f]{64}$") {
      throw "Buildx did not return a registry manifest digest for $Service."
    }
    Write-Output "$RepositoryUrl@$digest"
  }
  finally {
    if (Test-Path -LiteralPath $metadataPath) {
      Remove-Item -LiteralPath $metadataPath -Force
    }
  }
}

Publish-Image "api" $ApiRepository
Publish-Image "worker" $WorkerRepository
