param(
  [Parameter(Mandatory = $true)][string]$Region,
  [Parameter(Mandatory = $true)][string]$Cluster,
  [Parameter(Mandatory = $true)][string]$TaskDefinitionArn,
  [Parameter(Mandatory = $true)][string[]]$PrivateSubnets,
  [Parameter(Mandatory = $true)][string[]]$TaskSecurityGroups
)

$ErrorActionPreference = "Stop"

if ($PrivateSubnets.Count -lt 2 -or $TaskSecurityGroups.Count -lt 1) {
  throw "Migration tasks require at least two private subnets and one backend task security group."
}
if ($TaskDefinitionArn -notmatch ":task-definition/.+:\d+$") {
  throw "TaskDefinitionArn must identify a concrete, registered migration task revision."
}

$networkConfiguration = @{
  awsvpcConfiguration = @{
    subnets        = $PrivateSubnets
    securityGroups = $TaskSecurityGroups
    assignPublicIp = "DISABLED"
  }
} | ConvertTo-Json -Compress -Depth 4

$run = & aws ecs run-task --region $Region --cluster $Cluster --task-definition $TaskDefinitionArn --launch-type FARGATE --network-configuration $networkConfiguration --output json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $run.failures.Count -gt 0 -or $run.tasks.Count -ne 1) {
  throw "ECS did not start exactly one migration task."
}
$taskArn = $run.tasks[0].taskArn
& aws ecs wait tasks-stopped --region $Region --cluster $Cluster --tasks $taskArn
if ($LASTEXITCODE -ne 0) {
  throw "Migration task did not stop before the ECS waiter timed out."
}

$task = & aws ecs describe-tasks --region $Region --cluster $Cluster --tasks $taskArn --output json | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $task.tasks.Count -ne 1) {
  throw "Could not verify the stopped migration task."
}
$container = $task.tasks[0].containers | Where-Object { $_.name -eq "migrate" } | Select-Object -First 1
if ($null -eq $container -or $container.exitCode -ne 0) {
  throw "Migration task failed. Inspect its CloudWatch migration log stream before proceeding."
}

Write-Output "Migration task completed successfully: $taskArn"
