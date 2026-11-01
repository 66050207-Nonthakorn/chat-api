$ErrorActionPreference = "Stop"

$moduleDirectory = $PSScriptRoot
$planDirectory = Join-Path $moduleDirectory ".plan-only"
New-Item -ItemType Directory -Force -Path $planDirectory | Out-Null

Get-ChildItem -LiteralPath $moduleDirectory -File |
  Where-Object { $_.Extension -eq ".tf" -or $_.Name -in @(".terraform.lock.hcl", "terraform.tfvars.example") } |
  Copy-Item -Destination $planDirectory -Force

$versionsPath = Join-Path $planDirectory "versions.tf"
$versionsText = [System.IO.File]::ReadAllText($versionsPath)
$withoutRemoteBackend = [System.Text.RegularExpressions.Regex]::Replace(
  $versionsText,
  '(?ms)^\s*backend "s3"\s*\{.*?^\s*\}\s*\r?\n',
  ""
)
if ($withoutRemoteBackend -eq $versionsText) {
  throw "The isolated plan copy did not contain the expected S3 backend block."
}
[System.IO.File]::WriteAllText($versionsPath, $withoutRemoteBackend, [System.Text.UTF8Encoding]::new($false))

$formatArguments = @(
  "run", "--rm",
  "--mount", "type=bind,source=$moduleDirectory,target=/work",
  "-w", "/work",
  "hashicorp/terraform:1.17.1", "fmt", "-recursive"
)
& docker @formatArguments
if ($LASTEXITCODE -ne 0) {
  throw "Terraform fmt failed with exit code $LASTEXITCODE."
}

$validationCommand = "terraform init -backend=false && terraform validate && terraform plan -refresh=false -input=false -var-file=terraform.tfvars.example"
$planArguments = @(
  "run", "--rm",
  "--mount", "type=bind,source=$planDirectory,target=/work",
  "-w", "/work",
  "-e", "AWS_ACCESS_KEY_ID=plan-only",
  "-e", "AWS_SECRET_ACCESS_KEY=plan-only",
  "-e", "AWS_SHARED_CREDENTIALS_FILE=/dev/null",
  "-e", "AWS_CONFIG_FILE=/dev/null",
  "-e", "AWS_EC2_METADATA_DISABLED=true",
  "-e", "TF_VAR_valkey_auth_token=plan-only-placeholder-token",
  "--entrypoint", "/bin/sh",
  "hashicorp/terraform:1.17.1",
  "-c", $validationCommand
)
& docker @planArguments
if ($LASTEXITCODE -ne 0) {
  throw "Terraform init, validate, or plan failed with exit code $LASTEXITCODE."
}
