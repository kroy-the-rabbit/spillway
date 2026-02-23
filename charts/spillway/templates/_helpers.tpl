{{/*
Expand the name of the chart.
*/}}
{{- define "spillway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncate at 63 chars because some Kubernetes name fields are limited to this
(by the DNS naming spec).
*/}}
{{- define "spillway.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart label.
*/}}
{{- define "spillway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "spillway.labels" -}}
helm.sh/chart: {{ include "spillway.chart" . }}
{{ include "spillway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: spillway
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "spillway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "spillway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "spillway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "spillway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The container image reference.
The release pipeline tags images as "v{version}" (e.g. v0.1.2) while Chart.yaml
stores appVersion without the "v" prefix. When image.tag is empty we prepend "v"
so the default resolves correctly without an explicit --set image.tag override.
*/}}
{{- define "spillway.image" -}}
{{- $tag := .Values.image.tag | default (printf "v%s" .Chart.AppVersion) }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
