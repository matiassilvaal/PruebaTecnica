# Equivalente de prepare-segments.sh para PowerShell.
#
# Copia el manifiesto y los segmentos .ts desde la carpeta provista a .\segments\,
# que es lo que el Dockerfile mete en la imagen.
#
# La verificación se hace CONTRA EL MANIFIESTO y no contra un número fijo: se
# comprueba que exista cada .ts que el .m3u8 nombra. Un conteo hardcodeado daría
# por buena una copia a la que le falta justo el archivo que el manifiesto pide,
# y el error aparecería recién al arrancar el contenedor.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true, Position = 0,
        HelpMessage = 'Carpeta que contiene segment.m3u8 y los .ts')]
    [string]$Origen
)

$ErrorActionPreference = 'Stop'

$Destino = 'segments'
$Manifiesto = 'segment.m3u8'

if (-not (Test-Path -LiteralPath $Origen -PathType Container)) {
    # Write-Host y no Write-Error: con $ErrorActionPreference = 'Stop' un
    # Write-Error lanza, y quien lo corra ve un volcado de PowerShell con
    # CategoryInfo y FullyQualifiedErrorId en vez del mensaje.
    Write-Host "error: no existe la carpeta $Origen"
    exit 1
}

$rutaManifiesto = Join-Path $Origen $Manifiesto
if (-not (Test-Path -LiteralPath $rutaManifiesto -PathType Leaf)) {
    Write-Host "error: no se encontró $rutaManifiesto"
    Write-Host "       ¿Es la carpeta correcta? Debe traer el manifiesto junto a los .ts"
    exit 1
}

# Los nombres de segmento son las líneas del manifiesto que no son etiquetas.
$segmentos = Get-Content -LiteralPath $rutaManifiesto |
    ForEach-Object { $_.Trim() } |
    Where-Object { $_ -notmatch '^#' -and $_ -match '\.ts$' }

if ($segmentos.Count -eq 0) {
    Write-Host "error: $Manifiesto no nombra ningún .ts"
    exit 1
}

# Se comprueba TODO antes de copiar nada: es preferible fallar con la lista
# completa de lo que falta que dejar el destino a medio llenar.
$faltan = $segmentos | Where-Object {
    -not (Test-Path -LiteralPath (Join-Path $Origen $_) -PathType Leaf)
}

if ($faltan.Count -gt 0) {
    Write-Host "error: el manifiesto nombra $($segmentos.Count) segmentos y faltan $($faltan.Count):"
    $faltan | ForEach-Object { Write-Host "  $_" }
    exit 1
}

if (-not (Test-Path -LiteralPath $Destino -PathType Container)) {
    New-Item -ItemType Directory -Path $Destino | Out-Null
}

Copy-Item -LiteralPath $rutaManifiesto -Destination $Destino -Force
foreach ($s in $segmentos) {
    Copy-Item -LiteralPath (Join-Path $Origen $s) -Destination $Destino -Force
}

$bytes = (Get-ChildItem -LiteralPath $Destino -File | Measure-Object -Property Length -Sum).Sum
$mb = [math]::Round($bytes / 1MB, 1)
Write-Host "listo: $($segmentos.Count) segmentos y el manifiesto en $Destino\ ($mb MB)"
Write-Host "siguiente: docker build -t zapping-live ."
